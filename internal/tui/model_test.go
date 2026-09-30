package tui

import (
	"context"
	"easygo-agent/internal/clientapi"
	"errors"
	"fmt"
	"github.com/charmbracelet/bubbletea"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeQueue is an in-memory clientapi.QueueManager. Tests publish run events;
// a terminal event records the final status and closes the run's stream.
type fakeQueue struct {
	mu     sync.Mutex
	runs   []clientapi.RunRecord
	events map[string]chan clientapi.Event
}

func newFakeQueue() *fakeQueue { return &fakeQueue{events: map[string]chan clientapi.Event{}} }

func (q *fakeQueue) Submit(_ context.Context, _, session, input, _ string) (clientapi.RunRecord, clientapi.RunHandle, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	run := clientapi.RunRecord{ID: fmt.Sprintf("run-%d", len(q.runs)+1), SessionID: session, Input: input, Status: clientapi.RunRunning}
	q.runs = append(q.runs, run)
	q.events[run.ID] = make(chan clientapi.Event, 16)
	q.events[run.ID] <- clientapi.Event{Kind: clientapi.EventRunning, RunID: run.ID, Status: clientapi.RunRunning}
	return run, nil, nil
}

func (q *fakeQueue) Get(_ context.Context, _, _, id string) (clientapi.RunRecord, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, run := range q.runs {
		if run.ID == id {
			return run, nil
		}
	}
	return clientapi.RunRecord{}, errors.New("run not found")
}

func (q *fakeQueue) List(context.Context, string, string, int) ([]clientapi.RunRecord, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	active := []clientapi.RunRecord{}
	for _, run := range q.runs {
		if run.Status == clientapi.RunQueued || run.Status == clientapi.RunRunning {
			active = append(active, run)
		}
	}
	return active, nil
}

func (q *fakeQueue) Cancel(_ context.Context, _, _, id string) (clientapi.RunRecord, error) {
	return q.publish(id, clientapi.Event{Kind: clientapi.EventCanceled, Status: clientapi.RunCanceled})
}

func (q *fakeQueue) publish(id string, event clientapi.Event) (clientapi.RunRecord, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := slices.IndexFunc(q.runs, func(run clientapi.RunRecord) bool { return run.ID == id })
	if i < 0 || (q.runs[i].Status != clientapi.RunQueued && q.runs[i].Status != clientapi.RunRunning) {
		return clientapi.RunRecord{}, errors.New("run is not active")
	}
	event.RunID = id
	q.events[id] <- event
	if event.IsTerminal() {
		q.runs[i].Status, q.runs[i].ResultText = event.Status, event.Text
		close(q.events[id])
	}
	return q.runs[i], nil
}

func (q *fakeQueue) Subscribe(_ context.Context, _, _, id string) (clientapi.Subscription, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	events, ok := q.events[id]
	if !ok {
		return nil, errors.New("run not found")
	}
	return fakeSubscription(events), nil
}

func (q *fakeQueue) Close() error { return nil }

type fakeSubscription chan clientapi.Event

func (s fakeSubscription) Events() <-chan clientapi.Event { return s }
func (s fakeSubscription) Close()                         {}

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
	queue := newFakeQueue()
	m := NewQueue(queue, "alice", "session-1")
	m.input.SetValue("hi")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("queue submit did not start")
	}
	for _, event := range []clientapi.Event{{Kind: clientapi.EventTextDelta, Text: "queued-"}, {Kind: clientapi.EventCompleted, Status: clientapi.RunCompleted, Text: "queued-hello"}} {
		if _, err := queue.publish("run-1", event); err != nil {
			t.Fatal(err)
		}
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
	m.applyQueueEvent(queueEventMessage{runID: "canceled-before-output", ok: true, event: clientapi.Event{Kind: clientapi.EventCanceled}})
	if !strings.Contains(m.transcript(), "canceled") || m.state != stateIdle {
		t.Fatalf("cancellation was invisible: %q", m.transcript())
	}
}

func TestQueueTUICancelStopsRunning(t *testing.T) {
	queue := newFakeQueue()
	m := NewQueue(queue, "alice", "session-1")
	m.input.SetValue("hi")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("queue submit did not start")
	}
	if _, err := queue.publish("run-1", clientapi.Event{Kind: clientapi.EventTextDelta, Text: "partial"}); err != nil {
		t.Fatal(err)
	}
	runID := m.activeRunID
	if runID == "" && len(m.queueItems) > 0 {
		runID = m.queueItems[0].ID
	}
	if runID == "" {
		t.Fatal("tui did not record the running id")
	}
	if _, err := queue.Cancel(context.Background(), "alice", "session-1", runID); err != nil {
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
	got := formatToolStarted(clientapi.Event{
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
	got := formatToolFinished(clientapi.Event{
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
	model.queueItems = []clientapi.RunRecord{
		{ID: "running-id", Input: "计算 12 * 34", Status: clientapi.RunRunning},
		{ID: "queued-id", Input: "查询上海天气", Status: clientapi.RunQueued, Position: 1},
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

func TestRestoreAppendsRenderedHistory(t *testing.T) {
	model := New(nil)
	model.lines = []string{"pre-existing line"}
	model.Restore("alice", "sess-1", 9, []string{"you: hello", "assistant: 42", "run: failed"})
	want := []string{"pre-existing line", "user: alice · session: sess-1", "you: hello", "assistant: 42", "run: failed"}
	if !slices.Equal(model.lines, want) || model.notificationAfter != 9 || model.username != "alice" || model.sessionID != "sess-1" {
		t.Fatalf("restore: %q after=%d user=%q session=%q", model.lines, model.notificationAfter, model.username, model.sessionID)
	}
	model.Restore("alice", "sess-1", 3, nil)
	if model.notificationAfter != 9 {
		t.Fatalf("restore moved the notification cursor back to %d", model.notificationAfter)
	}
}
