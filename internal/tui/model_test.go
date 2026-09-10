package tui

import (
	"context"
	deepagent "easygo-agent/internal/agent/deepagent.go"
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

type fakeConversation struct{ run *fakeRun }

func (f fakeConversation) Start(string) (agentruntime.Run, error) { return f.run, nil }

type fakeRun struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (f *fakeRun) Next() agentruntime.Event {
	<-f.ctx.Done()
	return agentruntime.Event{Kind: agentruntime.EventCanceled}
}
func (f *fakeRun) Cancel() { f.cancel() }
func (f *fakeRun) Close() agentruntime.Event {
	f.Cancel()
	return agentruntime.Event{Kind: agentruntime.EventCanceled}
}

func TestUIRemainsCancelableDuringRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(fakeConversation{&fakeRun{ctx, cancel}})
	m.input.SetValue("hi")
	cmd := m.submit()
	if cmd == nil || m.state != stateRunning {
		t.Fatal("run did not start")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case message := <-done:
		m.Update(message)
	case <-time.After(time.Second):
		t.Fatal("cancel blocked")
	}
	if m.state != stateIdle || m.activeRun != nil {
		t.Fatal("cancel did not restore idle state")
	}
}

func TestUICloseAuditsAndReleasesActiveRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	model := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		return testutil.Text("partial"), nil
	}}
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Agent: config.AgentConfig{MaxSteps: 3, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	m := New(agentruntime.NewStored(ctx, agent, store, "alice", session.ID))
	m.input.SetValue("hi")
	command := m.submit()
	if command == nil {
		t.Fatal("run did not start")
	}
	m.Update(command())
	if m.partial != "partial" {
		t.Fatalf("partial output=%q", m.partial)
	}
	// Simulate external program exit without executing another tea.Cmd.
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("UI Close did not finish")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Begin(ctx, "alice", session.ID)
	if err != nil {
		t.Fatalf("UI exit left session locked: %v", err)
	}
	if len(lease.Messages()) != 1 {
		t.Fatal("canceled output entered model context")
	}
	lease.Close()
	turns, err := store.History(ctx, "alice", session.ID, 0, 10)
	if err != nil || len(turns) != 1 || turns[0].Status != "canceled" || len(turns[0].Messages) != 2 {
		t.Fatalf("UI exit audit=%+v err=%v", turns, err)
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
