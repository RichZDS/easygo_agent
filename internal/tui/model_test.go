package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"easygo-agent/internal/gateway"
	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
)

type fakeRunner struct {
	stream gateway.EventStream
	calls  int
	ctx    context.Context
}

// Run records one request and returns the configured fake stream.
func (runner *fakeRunner) Run(ctx context.Context, _ gateway.Request) (gateway.EventStream, error) {
	runner.calls++
	runner.ctx = ctx
	return runner.stream, nil
}

type fakeStream struct {
	events []gateway.Event
	index  int
	closed bool
}

// Recv returns deterministic events followed by EOF.
func (stream *fakeStream) Recv() (gateway.Event, error) {
	if stream.index == len(stream.events) {
		err := io.EOF
		zap.NewNop().Error("fake stream exhausted", zap.Error(err))
		return gateway.Event{}, err
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}

// Close records that the TUI released the stream.
func (stream *fakeStream) Close() error {
	stream.closed = true
	return nil
}

// TestCompletedRunAppendsHistory verifies only authoritative completed text is reused.
func TestCompletedRunAppendsHistory(t *testing.T) {
	t.Parallel()

	stream := &fakeStream{}
	runner := &fakeRunner{stream: stream}
	model := New(runner, "system", zap.NewNop())
	model.input.SetValue("hello")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = applyGatewayEvent(t, model, gateway.Event{Kind: gateway.EventTextDelta, Text: "partial"})
	model = applyGatewayEvent(t, model, gateway.Event{Kind: gateway.EventCompleted, Text: "final"})

	if got := model.history[len(model.history)-1]; got.Role != gateway.RoleAssistant || got.Content != "final" {
		t.Fatalf("last history message = %#v", got)
	}
	if strings.Contains(model.transcript(), "assistant: partial") || !strings.Contains(model.transcript(), "assistant: final") {
		t.Fatalf("transcript = %q", model.transcript())
	}
}

// TestFailedRunDoesNotAppendPartialHistory verifies failed partial output remains display-only.
func TestFailedRunDoesNotAppendPartialHistory(t *testing.T) {
	t.Parallel()

	model := submittedModel(t, &fakeStream{})
	wantHistory := len(model.history)
	model = applyGatewayEvent(t, model, gateway.Event{Kind: gateway.EventTextDelta, Text: "partial"})
	model = applyGatewayEvent(t, model, gateway.Event{Kind: gateway.EventFailed, Err: errors.New("model failed")})

	if len(model.history) != wantHistory {
		t.Fatalf("history length = %d, want %d", len(model.history), wantHistory)
	}
	if !strings.Contains(model.transcript(), "partial") || !strings.Contains(model.transcript(), "model failed") {
		t.Fatalf("transcript = %q", model.transcript())
	}
}

// TestControlCCancelsWhileRunningAndQuitsWhileIdle verifies the two-stage key behavior.
func TestControlCCancelsWhileRunningAndQuitsWhileIdle(t *testing.T) {
	t.Parallel()

	stream := &fakeStream{}
	model := submittedModel(t, stream)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-model.runContext.Done():
	default:
		t.Fatal("running Ctrl+C did not cancel the run context")
	}
	model = applyGatewayEvent(t, model, gateway.Event{Kind: gateway.EventCanceled})
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

// TestDuplicateSubmitIsIgnored verifies only one active stream exists.
func TestDuplicateSubmitIsIgnored(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{stream: &fakeStream{}}
	model := New(runner, "system", zap.NewNop())
	model.input.SetValue("first")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model.input.SetValue("second")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if runner.calls != 1 {
		t.Fatalf("Runner calls = %d, want 1", runner.calls)
	}
}

// submittedModel returns a TUI model with one active run.
func submittedModel(t *testing.T, stream gateway.EventStream) *Model {
	t.Helper()

	runner := &fakeRunner{stream: stream}
	model := New(runner, "system", zap.NewNop())
	model.input.SetValue("hello")
	return updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
}

// updateModel applies one Bubble Tea message and returns the concrete model.
func updateModel(t *testing.T, model *Model, message tea.Msg) *Model {
	t.Helper()

	updated, _ := model.Update(message)
	concrete, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update() returned %T, want *Model", updated)
	}
	return concrete
}

// applyGatewayEvent routes one Gateway event through the Bubble Tea update path.
func applyGatewayEvent(t *testing.T, model *Model, event gateway.Event) *Model {
	t.Helper()

	return updateModel(t, model, gatewayEventMessage{event: event})
}
