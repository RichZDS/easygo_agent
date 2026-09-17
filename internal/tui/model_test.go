package tui

import (
	"context"
	"easygo-agent/internal/agent/deepagent"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"
	"github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
	"time"
)

func pumpTUI(t *testing.T, m *Model, cmd tea.Cmd, timeout time.Duration, pred func(*Model) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred(m) {
			return
		}
		if cmd == nil {
			cmd = m.waitForQueueEvents()
		}
		if cmd == nil {
			time.Sleep(time.Millisecond)
			continue
		}
		msg := cmd()
		var next tea.Cmd
		_, next = m.Update(msg)
		cmd = next
	}
	t.Fatal("tui did not reach expected state")
}

func TestQueueTUISubmitsAndObservesCompleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	model := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		return testutil.Text("queued-hello"), nil
	}}
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Agent: config.AgentConfig{MaxSteps: 3, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	manager := agentruntime.NewQueueManager(ctx, store, agent, agentruntime.QueueConfig{MaxWorkers: 1, PollInterval: time.Millisecond, LeaseTTL: time.Second})
	defer manager.Close()
	m := NewQueue(manager, "alice", session.ID)
	m.input.SetValue("hi")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("queue submit did not start")
	}
	pumpTUI(t, m, cmd, 5*time.Second, func(m *Model) bool {
		return strings.Contains(m.transcript(), "queued-hello")
	})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestQueueCancellationWithoutOutputIsVisible(t *testing.T) {
	m := New(nil)
	m.applyQueueEvent(queueEventMessage{runID: "canceled-before-output", ok: true, event: agentruntime.Event{Kind: agentruntime.EventCanceled}})
	if !strings.Contains(m.transcript(), "canceled") || m.state != stateIdle {
		t.Fatalf("cancellation was invisible: %q", m.transcript())
	}
}

func TestQueueTUICancelStopsRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	model := &testutil.Model{StreamFunc: func(ctx context.Context, _ []*schema.AgenticMessage) (*schema.StreamReader[*schema.AgenticMessage], error) {
		reader, writer := schema.Pipe[*schema.AgenticMessage](1)
		go func() {
			defer writer.Close()
			writer.Send(testutil.Text("partial"), nil)
			close(started)
			<-ctx.Done()
		}()
		return reader, nil
	}}
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Agent: config.AgentConfig{MaxSteps: 3, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	manager := agentruntime.NewQueueManager(ctx, store, agent, agentruntime.QueueConfig{MaxWorkers: 1, PollInterval: time.Millisecond, LeaseTTL: time.Second})
	defer manager.Close()
	m := NewQueue(manager, "alice", session.ID)
	m.input.SetValue("hi")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("queue submit did not start")
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("queued tui run did not start")
	}
	runID := m.activeRunID
	if runID == "" && len(m.queueItems) > 0 {
		runID = m.queueItems[0].ID
	}
	if runID == "" {
		t.Fatal("tui did not record the running id")
	}
	if _, err := manager.Cancel(ctx, "alice", session.ID, runID); err != nil {
		t.Fatal(err)
	}
	pumpTUI(t, m, cmd, 5*time.Second, func(m *Model) bool {
		return strings.Contains(m.transcript(), "canceled") && len(m.queueSubs) == 0 && m.state == stateIdle
	})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestFormatToolStartedIncludesDetails 验证 TUI 展示 tool 调用的 name、call_id 与 arguments。
func TestFormatToolStartedIncludesDetails(t *testing.T) {
	got := formatToolStarted(agentruntime.Event{
		Tool:      "calculator",
		CallID:    "c1",
		Arguments: `{"operation":"multiply"}`,
	})
	for _, want := range []string{"tool started: calculator", "call_id: c1", `arguments: {"operation":"multiply"}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatToolStarted missing %q in %q", want, got)
		}
	}
}

// TestFormatToolFinishedIncludesDetails 验证 TUI 展示 tool 结果的 name、call_id 与 result。
func TestFormatToolFinishedIncludesDetails(t *testing.T) {
	got := formatToolFinished(agentruntime.Event{
		Tool:   "calculator",
		CallID: "c1",
		Result: `{"result":42}`,
	})
	for _, want := range []string{"tool completed: calculator", "call_id: c1", `result: {"result":42}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatToolFinished missing %q in %q", want, got)
		}
	}
}

// TestTranscriptIncludesStreamingReasoning 验证进行中的 reasoning 会出现在 transcript。
func TestTranscriptIncludesStreamingReasoning(t *testing.T) {
	model := New(nil)
	model.reasoning = "The user wants a calculation"
	got := model.transcript()
	if !strings.Contains(got, "reasoning: The user wants a calculation") {
		t.Fatalf("transcript missing reasoning: %q", got)
	}
}

func TestQueueViewShowsBannerAndStatuses(t *testing.T) {
	model := New(nil)
	model.queueMode = true
	model.queueItems = []conversation.RunRecord{
		{ID: "running-id", Input: "计算 12 * 34", Status: conversation.RunRunning},
		{ID: "queued-id", Input: "查询上海天气", Status: conversation.RunQueued, Position: 1},
	}
	model.refreshViewport()
	view := model.View()
	for _, want := range []string{"EASY GO", "running: 1 · queued: 1", "[running]", "[queued]", "计算 12 * 34"} {
		if !strings.Contains(view, want) {
			t.Fatalf("queue view missing %q: %q", want, view)
		}
	}
}

func TestQueueTabSwitchesInputFocus(t *testing.T) {
	model := New(nil)
	model.queueMode = true
	if !model.input.Focused() {
		t.Fatal("input should start focused")
	}
	model.Update(tea.KeyMsg{Type: tea.KeyTab})
	if model.input.Focused() || !model.queueFocus {
		t.Fatal("tab did not move focus to queue")
	}
	model.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !model.input.Focused() || model.queueFocus {
		t.Fatal("second tab did not move focus to input")
	}
}
