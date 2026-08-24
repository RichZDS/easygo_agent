package tui

import (
	"errors"
	"strings"
	"testing"

	agentruntime "easygo-agent/internal/agent/runtime"

	tea "github.com/charmbracelet/bubbletea"
)

type fakeConversation struct {
	calls  int
	inputs []string
	run    *fakeRun
	err    error
}

func (conversation *fakeConversation) Start(input string) (agentruntime.Run, error) {
	conversation.calls++
	conversation.inputs = append(conversation.inputs, input)
	if conversation.err != nil {
		return nil, conversation.err
	}
	if conversation.run == nil {
		conversation.run = &fakeRun{}
	}
	return conversation.run, nil
}

type fakeRun struct {
	events   []agentruntime.Event
	next     int
	canceled bool
}

func (run *fakeRun) Next() agentruntime.Event {
	if run.next >= len(run.events) {
		return agentruntime.Event{Kind: agentruntime.EventCompleted}
	}
	event := run.events[run.next]
	run.next++
	return event
}

func (run *fakeRun) Cancel() {
	run.canceled = true
}

// TestCompletedRunAppearsInTranscript 验证完整助手文本会固定到 transcript。
func TestCompletedRunAppearsInTranscript(t *testing.T) {
	t.Parallel()

	model := submittedModel(t)
	model = applyRunEvent(t, model, agentruntime.Event{Kind: agentruntime.EventTextDelta, Text: "fin"})
	model = applyRunEvent(t, model, agentruntime.Event{Kind: agentruntime.EventCompleted, Text: "final"})

	if !strings.Contains(model.transcript(), "assistant: final") {
		t.Fatalf("transcript = %q", model.transcript())
	}
	if model.state != stateIdle {
		t.Fatalf("state = %v, want idle", model.state)
	}
}

// TestFailedRunKeepsPartialOutput 验证失败时的部分输出只用于展示。
func TestFailedRunKeepsPartialOutput(t *testing.T) {
	t.Parallel()

	model := submittedModel(t)
	model = applyRunEvent(t, model, agentruntime.Event{
		Kind: agentruntime.EventFailed,
		Text: "partial",
		Err:  errors.New("model failed"),
	})

	if !strings.Contains(model.transcript(), "assistant (partial): partial") || !strings.Contains(model.transcript(), "model failed") {
		t.Fatalf("transcript = %q", model.transcript())
	}
}

// TestControlCCancelsWhileRunningAndQuitsWhileIdle 验证两阶段按键行为。
func TestControlCCancelsWhileRunningAndQuitsWhileIdle(t *testing.T) {
	t.Parallel()

	run := &fakeRun{}
	model := submittedModelWith(t, &fakeConversation{run: run})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !run.canceled {
		t.Fatal("running Ctrl+C did not cancel the active run")
	}
	model = applyRunEvent(t, model, agentruntime.Event{Kind: agentruntime.EventCanceled})
	if model.state != stateIdle {
		t.Fatalf("state = %v, want idle", model.state)
	}

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("idle Ctrl+C command = nil, want tea.Quit")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("idle Ctrl+C command message is not tea.QuitMsg")
	}
}

// TestDuplicateSubmitIsIgnored 验证 TUI 在运行中不会发起第二次会话运行。
func TestDuplicateSubmitIsIgnored(t *testing.T) {
	t.Parallel()

	conversation := &fakeConversation{}
	model := New(conversation)
	model.input.SetValue("first")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model.input.SetValue("second")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if conversation.calls != 1 {
		t.Fatalf("Start calls = %d, want 1", conversation.calls)
	}
}

// TestToolEventsAppearInTranscript 验证 Tool 语义事件只负责展示。
func TestToolEventsAppearInTranscript(t *testing.T) {
	t.Parallel()

	model := submittedModel(t)
	model = applyRunEvent(t, model, agentruntime.Event{Kind: agentruntime.EventToolStarted, Tool: "calculator"})
	model = applyRunEvent(t, model, agentruntime.Event{Kind: agentruntime.EventToolFinished, Tool: "calculator"})

	if !strings.Contains(model.transcript(), "tool: calculator started") {
		t.Fatalf("transcript missing tool start: %q", model.transcript())
	}
	if !strings.Contains(model.transcript(), "tool: calculator completed") {
		t.Fatalf("transcript missing tool completion: %q", model.transcript())
	}
}

// TestStartFailureIsRendered 验证会话模块启动失败时 TUI 回到可输入状态。
func TestStartFailureIsRendered(t *testing.T) {
	t.Parallel()

	model := New(&fakeConversation{err: errors.New("cannot start")})
	model.input.SetValue("hello")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.state != stateIdle || !strings.Contains(model.transcript(), "cannot start") {
		t.Fatalf("state = %v, transcript = %q", model.state, model.transcript())
	}
}

func submittedModel(t *testing.T) *Model {
	t.Helper()
	return submittedModelWith(t, &fakeConversation{})
}

func submittedModelWith(t *testing.T, conversation *fakeConversation) *Model {
	t.Helper()
	model := New(conversation)
	model.input.SetValue("hello")
	return updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
}

func updateModel(t *testing.T, model *Model, message tea.Msg) *Model {
	t.Helper()
	updated, _ := model.Update(message)
	concrete, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update() returned %T, want *Model", updated)
	}
	return concrete
}

func applyRunEvent(t *testing.T, model *Model, event agentruntime.Event) *Model {
	t.Helper()
	return updateModel(t, model, runEventMessage{event: event})
}
