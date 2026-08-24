package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	tea "github.com/charmbracelet/bubbletea"
)

type fakeAgent struct {
	calls int
}

func (agent *fakeAgent) Name(context.Context) string { return "fake" }

func (agent *fakeAgent) Description(context.Context) string { return "fake" }

// Run 记录一次调用并立即关闭迭代器；测试通过 Update 注入事件。
func (agent *fakeAgent) Run(context.Context, *adk.TypedAgentInput[*schema.AgenticMessage], ...adk.AgentRunOption) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
	agent.calls++
	iter, gen := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
	gen.Close()
	return iter
}

// TestCompletedRunAppendsHistory 验证完整助手文本会写入下一轮历史。
func TestCompletedRunAppendsHistory(t *testing.T) {
	t.Parallel()

	agent := &fakeAgent{}
	model := New(agent, "system")
	model.input.SetValue("hello")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = applyAgentEvent(t, model, adk.EventFromAgenticMessage(assistantAgenticMessage("final"), nil, schema.AgenticRoleTypeAssistant))
	model = applyAgentEnded(t, model)

	if got := model.history[len(model.history)-1]; got.Role != schema.AgenticRoleTypeAssistant || assistantText(got) != "final" {
		t.Fatalf("last history message = %#v", got)
	}
	if !strings.Contains(model.transcript(), "assistant: final") {
		t.Fatalf("transcript = %q", model.transcript())
	}
}

// TestFailedRunDoesNotAppendPartialHistory 验证失败时的部分输出只用于展示。
func TestFailedRunDoesNotAppendPartialHistory(t *testing.T) {
	t.Parallel()

	model := submittedModel(t)
	wantHistory := len(model.history)
	model = applyAgentEvent(t, model, adk.EventFromAgenticMessage(assistantAgenticMessage("partial"), nil, schema.AgenticRoleTypeAssistant))
	model = applyAgentEvent(t, model, &adk.TypedAgentEvent[*schema.AgenticMessage]{Err: errors.New("model failed")})

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

	model := submittedModel(t)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-model.runContext.Done():
	default:
		t.Fatal("running Ctrl+C did not cancel the run context")
	}
	model = applyAgentEnded(t, model)
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

	agent := &fakeAgent{}
	model := New(agent, "system")
	model.input.SetValue("first")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model.input.SetValue("second")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if agent.calls != 1 {
		t.Fatalf("Agent calls = %d, want 1", agent.calls)
	}
}

// TestAgenticToolBlocksAppearInTranscript 验证 Tool 调用与结果走 ContentBlock。
func TestAgenticToolBlocksAppearInTranscript(t *testing.T) {
	t.Parallel()

	model := submittedModel(t)
	model = applyAgentEvent(t, model, adk.EventFromAgenticMessage(&schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolCall{Name: "calculator", CallID: "call-1"}),
		},
	}, nil, schema.AgenticRoleTypeAssistant))
	model = applyAgentEvent(t, model, adk.EventFromAgenticMessage(&schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolResult{Name: "calculator", CallID: "call-1"}),
		},
	}, nil, schema.AgenticRoleTypeUser))

	if !strings.Contains(model.transcript(), "tool: calculator started") {
		t.Fatalf("transcript missing tool start: %q", model.transcript())
	}
	if !strings.Contains(model.transcript(), "tool: calculator completed") {
		t.Fatalf("transcript missing tool completion: %q", model.transcript())
	}
}

// submittedModel 返回已有一次活动运行的 TUI 模型。
func submittedModel(t *testing.T) *Model {
	t.Helper()

	model := New(&fakeAgent{}, "system")
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

// applyAgentEvent 通过 Bubble Tea 更新路径投递一条原生 ADK 事件。
func applyAgentEvent(t *testing.T, model *Model, event *adk.TypedAgentEvent[*schema.AgenticMessage]) *Model {
	t.Helper()

	return updateModel(t, model, agentEventMessage{event: event})
}

// applyAgentEnded 通过 Bubble Tea 更新路径投递迭代器结束。
func applyAgentEnded(t *testing.T, model *Model) *Model {
	t.Helper()

	return updateModel(t, model, agentEventMessage{ended: true})
}
