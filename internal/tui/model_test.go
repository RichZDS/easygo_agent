package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"easygo-agent/internal/gateway"
	"easygo-agent/internal/logger"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
)

type fakeRunner struct {
	stream gateway.EventStream
	calls  int
	ctx    context.Context
}

// Run 记录一次请求并返回配置好的假流。
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

// Recv 返回确定性事件，随后返回 EOF。
func (stream *fakeStream) Recv() (gateway.Event, error) {
	if stream.index == len(stream.events) {
		err := io.EOF
		logger.Error("fake stream exhausted", zap.Error(err))
		return gateway.Event{}, err
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}

// Close 记录 TUI 已释放该流。
func (stream *fakeStream) Close() error {
	stream.closed = true
	return nil
}

// TestCompletedRunAppendsHistory 验证只有权威完成文本会被复用。
func TestCompletedRunAppendsHistory(t *testing.T) {
	t.Parallel()

	stream := &fakeStream{}
	runner := &fakeRunner{stream: stream}
	model := New(runner, "system")
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

// TestFailedRunDoesNotAppendPartialHistory 验证失败时的部分输出只用于展示。
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

// TestControlCCancelsWhileRunningAndQuitsWhileIdle 验证两阶段按键行为。
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

// TestDuplicateSubmitIsIgnored 验证同一时刻只有一条活动流。
func TestDuplicateSubmitIsIgnored(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{stream: &fakeStream{}}
	model := New(runner, "system")
	model.input.SetValue("first")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model.input.SetValue("second")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if runner.calls != 1 {
		t.Fatalf("Runner calls = %d, want 1", runner.calls)
	}
}

// submittedModel 返回已有一次活动运行的 TUI 模型。
func submittedModel(t *testing.T, stream gateway.EventStream) *Model {
	t.Helper()

	runner := &fakeRunner{stream: stream}
	model := New(runner, "system")
	model.input.SetValue("hello")
	return updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
}

// updateModel 应用一条 Bubble Tea 消息并返回具体模型。
func updateModel(t *testing.T, model *Model, message tea.Msg) *Model {
	t.Helper()

	updated, _ := model.Update(message)
	concrete, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update() returned %T, want *Model", updated)
	}
	return concrete
}

// applyGatewayEvent 通过 Bubble Tea 更新路径投递一条 Gateway 事件。
func applyGatewayEvent(t *testing.T, model *Model, event gateway.Event) *Model {
	t.Helper()

	return updateModel(t, model, gatewayEventMessage{event: event})
}
