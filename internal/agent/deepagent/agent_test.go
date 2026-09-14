package deepagent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/internal/agent/deepagent"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"
	"easygo-agent/internal/tools"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestNewValidatesConstructionConfig(t *testing.T) {
	if _, err := deepagent.New(context.Background(), deepagent.Config{}); err == nil {
		t.Fatal("nil chat model was accepted")
	}
	if _, err := deepagent.New(context.Background(), deepagent.Config{
		ChatModel: &testutil.Model{},
	}); err == nil {
		t.Fatal("non-positive max steps was accepted")
	}
}

func collect(t *testing.T, run agentruntime.Run) []agentruntime.Event {
	t.Helper()
	var events []agentruntime.Event
	for i := 0; i < 100; i++ {
		e := run.Next()
		events = append(events, e)
		if e.Kind == agentruntime.EventCompleted || e.Kind == agentruntime.EventFailed || e.Kind == agentruntime.EventCanceled {
			return events
		}
	}
	t.Fatal("run did not terminate")
	return nil
}

func startRun(t *testing.T, ctx context.Context, store *conversation.Memory, agent adk.TypedAgent[*schema.AgenticMessage], user, sessionID, input string, memory ...conversation.MemoryStore) agentruntime.Run {
	t.Helper()
	run, err := agentruntime.ClaimQueuedRun(ctx, store, agent, user, sessionID, input, memory...)
	if err != nil {
		t.Fatal(err)
	}
	return run
}
func TestLoopPreservesNativeMessagesAcrossSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := conversation.NewMemory()
	s, _ := store.Create(ctx, "alice")
	var calls atomic.Int32
	model := &testutil.Model{GenerateFunc: func(_ context.Context, messages []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		switch calls.Add(1) {
		case 1:
			return testutil.ToolCall("call-1", `{"operation":"add","a":2,"b":3}`), nil
		case 2:
			result := messages[len(messages)-1].ContentBlocks[0].FunctionToolResult
			if result == nil || result.CallID != "call-1" || !strings.Contains(result.Content[0].Text.Text, "5") {
				return nil, errors.New("missing native tool result")
			}
			m := testutil.Text("5")
			m.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{TotalTokens: 100}}
			return m, nil
		default:
			var systems, toolCalls, results int
			for _, m := range messages {
				if m.Role == schema.AgenticRoleTypeSystem {
					systems++
				}
				for _, b := range m.ContentBlocks {
					if b.FunctionToolCall != nil {
						toolCalls++
					}
					if b.FunctionToolResult != nil {
						results++
					}
				}
			}
			if systems != 1 || toolCalls != 1 || results != 1 {
				return nil, errors.New("lost history or duplicated system prompt")
			}
			return testutil.Text("remembered 5"), nil
		}
	}}
	calculator, err := tools.NewCalculator()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 5, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"2+3?", "remember the result?"} {
		run := startRun(t, ctx, store, agent, "alice", s.ID, input)
		events := collect(t, run)
		last := events[len(events)-1]
		if last.Kind != agentruntime.EventCompleted {
			t.Fatalf("terminal = %+v", last)
		}
	}
	turns, err := store.History(ctx, "alice", s.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || len(turns[0].Messages) != 4 {
		t.Fatalf("turns = %+v; first messages=%d", turns, len(turns[0].Messages))
	}
	if turns[0].Messages[3].ResponseMeta.TokenUsage.TotalTokens != 100 {
		t.Fatal("usage lost")
	}
	if calls.Load() != 3 {
		t.Fatalf("model calls=%d", calls.Load())
	}
}

func TestCompressionAfterToolResultPersistsNativeState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := conversation.NewMemory()
	s, _ := store.Create(ctx, "alice")
	var mainCalls, summaryCalls atomic.Int32
	main := &testutil.Model{GenerateFunc: func(_ context.Context, messages []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		if mainCalls.Add(1) == 1 {
			m := testutil.ToolCall("c1", `{"operation":"multiply","a":2,"b":4}`)
			m.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{TotalTokens: 40000}}
			return m, nil
		}
		var joined strings.Builder
		for _, m := range messages {
			joined.WriteString(m.String())
		}
		if !strings.Contains(joined.String(), "summary-marker") {
			return nil, errors.New("compressed state missing")
		}
		return testutil.Text("8"), nil
	}}
	summary := &testutil.Model{GenerateFunc: func(_ context.Context, messages []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		summaryCalls.Add(1)
		var joined strings.Builder
		for _, m := range messages {
			joined.WriteString(m.String())
		}
		if !strings.Contains(joined.String(), "calculator") {
			return nil, errors.New("summary missed tool history")
		}
		return testutil.Text("summary-marker: multiplication returned 8. Reply to the user's question."), nil
	}}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: main, SummaryModel: summary, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 5, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	run := startRun(t, ctx, store, agent, "alice", s.ID, "2*4?")
	events := collect(t, run)
	if last := events[len(events)-1]; last.Kind != agentruntime.EventCompleted {
		t.Fatalf("terminal=%+v", last)
	}
	var compressed bool
	for _, e := range events {
		if e.Kind == agentruntime.EventCompressed {
			compressed = true
		}
	}
	if !compressed || summaryCalls.Load() != 1 {
		t.Fatalf("compression missing: %v / %d", compressed, summaryCalls.Load())
	}
	lease, err := store.Begin(ctx, "alice", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.Messages()) != 3 {
		t.Fatalf("want system + summary + final, got %d", len(lease.Messages()))
	}
	lease.Close()
	run = startRun(t, ctx, store, agent, "alice", s.ID, "continue")
	events = collect(t, run)
	if last := events[len(events)-1]; last.Kind != agentruntime.EventCompleted {
		t.Fatalf("resume terminal=%+v", last)
	}
	turns, _ := store.History(ctx, "alice", s.ID, 0, 100)
	if len(turns[0].Messages) != 4 {
		t.Fatal("compaction removed raw transcript")
	}
}

func TestSummaryFailureAndIterationLimitDoNotCommitPartialContext(t *testing.T) {
	for _, summaryFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "iteration_limit", true: "summary_failure"}[summaryFailure], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := conversation.NewMemory()
			s, _ := store.Create(ctx, "alice")
			main := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
				m := testutil.ToolCall("c", `{"operation":"add","a":1,"b":1}`)
				if summaryFailure {
					m.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{TotalTokens: 40000}}
				}
				return m, nil
			}}
			summary := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
				return nil, errors.New("summary backend unavailable")
			}}
			calculator, _ := tools.NewCalculator()
			agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: main, SummaryModel: summary, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 2, ContextTokens: 24000}})
			if err != nil {
				t.Fatal(err)
			}
			run := startRun(t, ctx, store, agent, "alice", s.ID, "calculate")
			events := collect(t, run)
			if last := events[len(events)-1]; last.Kind != agentruntime.EventFailed {
				t.Fatalf("terminal=%+v", last)
			}
			lease, _ := store.Begin(ctx, "alice", s.ID)
			defer lease.Close()
			if len(lease.Messages()) != 1 || lease.Messages()[0].Role != schema.AgenticRoleTypeUser {
				t.Fatal("partial tool chain leaked into context")
			}
			turns, _ := store.History(ctx, "alice", s.ID, 0, 100)
			if len(turns) != 1 || turns[0].Status != "failed" {
				t.Fatal("failed turn not recorded")
			}
		})
	}
}

func TestSharedAgentRunsIndependentUsersConcurrently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := conversation.NewMemory()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	model := &testutil.Model{GenerateFunc: func(ctx context.Context, messages []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return testutil.Text(messages[len(messages)-1].ContentBlocks[0].UserInputText.Text), nil
	}}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 3, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan agentruntime.Event, 2)
	for _, user := range []string{"alice", "bob"} {
		s, _ := store.Create(ctx, user)
		run := startRun(t, ctx, store, agent, user, s.ID, user)
		go func() {
			for {
				event := run.Next()
				if event.Kind == agentruntime.EventCompleted || event.Kind == agentruntime.EventFailed || event.Kind == agentruntime.EventCanceled {
					results <- event
					return
				}
			}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("independent conversations were serialized")
		}
	}
	close(release)
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case event := <-results:
			if event.Kind != agentruntime.EventCompleted {
				t.Fatalf("terminal=%+v", event)
			}
			seen[event.Text] = true
		case <-ctx.Done():
			t.Fatal("concurrent run did not complete")
		}
	}
	if !seen["alice"] || !seen["bob"] {
		t.Fatalf("cross-user state leakage: %v", seen)
	}
}

// This is an end-to-end runtime assertion: a real Eino Deep Agent receives
// exactly the user-scoped ranked profile before its model invocation. Both TUI
// and HTTP Gateway construct this same Stored runtime.
func TestDeepAgentReceivesUserLongTermMemory(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	memories := conversation.NewMemoryLongTerm()
	defer memories.Close()
	profile, err := memories.ReplaceProfile(ctx, "alice", []conversation.MemoryDraft{{
		Kind: conversation.MemoryKindStyle, Content: "始终优先使用简洁中文", Importance: .9, Confidence: .95,
		SourceSessions: []string{session.ID}, SourceTurnIDs: []int64{1},
	}})
	if err != nil || len(profile) != 1 {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	model := &testutil.Model{GenerateFunc: func(_ context.Context, messages []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		var hasMemory bool
		for _, message := range messages {
			if message.Role == schema.AgenticRoleTypeSystem && strings.Contains(message.String(), "始终优先使用简洁中文") && strings.Contains(message.String(), profile[0].ID) {
				hasMemory = true
			}
		}
		if !hasMemory {
			return nil, errors.New("ranked user memory was not injected")
		}
		return testutil.Text("收到"), nil
	}}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 2, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	run := startRun(t, ctx, store, agent, "alice", session.ID, "你好", memories)
	events := collect(t, run)
	if last := events[len(events)-1]; last.Kind != agentruntime.EventCompleted || last.Text != "收到" {
		t.Fatalf("terminal=%+v", last)
	}
	updated, _ := memories.ActiveProfile(ctx, "alice")
	if updated[0].CallCount != 1 {
		t.Fatalf("injected memory was not counted exactly once: %+v", updated[0])
	}
}

// The acceptance suite exercises the production Eino Agent + Stored runtime
// for 200 independent users. It pre-registers the expected memory for every
// case and fails below 95%; 200/200 makes ranking, injection, isolation and
// retrieval accounting measurable rather than a one-off manual check.
func TestUserMemoryEndToEndAcceptanceAtLeastNinetyFivePercent(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	memories := conversation.NewMemoryLongTerm()
	defer memories.Close()
	model := &testutil.Model{GenerateFunc: func(_ context.Context, messages []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		var requested, remembered string
		for _, message := range messages {
			text := message.String()
			if message.Role == schema.AgenticRoleTypeUser {
				for _, block := range message.ContentBlocks {
					if block != nil && block.UserInputText != nil && strings.HasPrefix(block.UserInputText.Text, "case-") {
						requested = block.UserInputText.Text
					}
				}
			}
			if message.Role == schema.AgenticRoleTypeSystem {
				for i := 0; i < 200; i++ {
					candidate := fmt.Sprintf("preference-%03d", i)
					if strings.Contains(text, candidate) {
						remembered = candidate
						break
					}
				}
			}
		}
		want := "preference-" + strings.TrimPrefix(requested, "case-")
		if remembered != want {
			return nil, fmt.Errorf("memory isolation/injection mismatch: got %q want %q", remembered, want)
		}
		return testutil.Text(remembered), nil
	}}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 2, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	const cases = 200
	passed := 0
	for i := 0; i < cases; i++ {
		user := fmt.Sprintf("user-%03d", i)
		session, createErr := store.Create(ctx, user)
		if createErr != nil {
			t.Fatal(createErr)
		}
		preference := fmt.Sprintf("preference-%03d", i)
		_, replaceErr := memories.ReplaceProfile(ctx, user, []conversation.MemoryDraft{{
			Kind: conversation.MemoryKindPreference, Content: preference, Importance: .9, Confidence: .99,
			SourceSessions: []string{session.ID}, SourceTurnIDs: []int64{1},
		}})
		if replaceErr != nil {
			t.Fatal(replaceErr)
		}
		run := startRun(t, ctx, store, agent, user, session.ID, fmt.Sprintf("case-%03d", i), memories)
		events := collect(t, run)
		last := events[len(events)-1]
		if last.Kind == agentruntime.EventCompleted && last.Text == preference {
			passed++
		}
	}
	accuracy := float64(passed) / cases
	t.Logf("user-memory E2E accuracy: %.2f%% (%d/%d)", accuracy*100, passed, cases)
	if accuracy < .95 {
		t.Fatalf("user-memory E2E accuracy %.2f%% (%d/%d), need >=95%%", accuracy*100, passed, cases)
	}
}
