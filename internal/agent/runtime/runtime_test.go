package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type fakeAgent struct {
	calls     int
	lastInput *adk.TypedAgentInput[*schema.AgenticMessage]
	responses []*adk.TypedAgentEvent[*schema.AgenticMessage]
}

func (agent *fakeAgent) Name(context.Context) string { return "fake" }

func (agent *fakeAgent) Description(context.Context) string { return "fake" }

func (agent *fakeAgent) Run(_ context.Context, input *adk.TypedAgentInput[*schema.AgenticMessage], _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
	agent.calls++
	agent.lastInput = input
	iterator, generator := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
	go func() {
		defer generator.Close()
		for _, event := range agent.responses {
			generator.Send(event)
		}
	}()
	return iterator
}

// TestCompletedRunOwnsHistory 验证会话模块，而非 TUI，维护完整对话历史。
func TestCompletedRunOwnsHistory(t *testing.T) {
	t.Parallel()

	agent := &fakeAgent{responses: []*adk.TypedAgentEvent[*schema.AgenticMessage]{
		adk.EventFromAgenticMessage(assistantMessage("final"), nil, schema.AgenticRoleTypeAssistant),
	}}
	session := New(context.Background(), agent, "system")
	run, err := session.Start("hello")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	events := receiveAll(run)

	if len(events) != 2 || events[0].Kind != EventTextDelta || events[1].Kind != EventCompleted {
		t.Fatalf("events = %#v", events)
	}
	if events[1].Text != "final" {
		t.Fatalf("completed text = %q, want final", events[1].Text)
	}
	if agent.lastInput == nil || !agent.lastInput.EnableStreaming {
		t.Fatalf("Agent input = %#v, want streaming enabled", agent.lastInput)
	}
	if len(agent.lastInput.Messages) != 2 || agent.lastInput.Messages[0].Role != schema.AgenticRoleTypeSystem || agent.lastInput.Messages[1].Role != schema.AgenticRoleTypeUser {
		t.Fatalf("Agent messages = %#v", agent.lastInput.Messages)
	}
	if len(session.history) != 3 || session.history[2].Role != schema.AgenticRoleTypeAssistant || assistantText(session.history[2]) != "final" {
		t.Fatalf("session history = %#v", session.history)
	}
}

// TestStreamingMessagesBecomeTextEvents 验证 Eino 消息流完全封装在运行模块内。
func TestStreamingMessagesBecomeTextEvents(t *testing.T) {
	t.Parallel()

	stream := schema.StreamReaderFromArray([]*schema.AgenticMessage{
		assistantMessage("hel"),
		assistantMessage("lo"),
	})
	agent := &fakeAgent{responses: []*adk.TypedAgentEvent[*schema.AgenticMessage]{
		adk.EventFromAgenticMessage(nil, stream, schema.AgenticRoleTypeAssistant),
	}}
	run, err := New(context.Background(), agent, "").Start("question")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	events := receiveAll(run)

	if len(events) != 3 || events[0].Text != "hel" || events[1].Text != "lo" || events[2].Kind != EventCompleted || events[2].Text != "hello" {
		t.Fatalf("events = %#v", events)
	}
}

// TestCanceledRunDoesNotCommitAssistantHistory 验证取消只保留用户输入，不提交部分回复。
func TestCanceledRunDoesNotCommitAssistantHistory(t *testing.T) {
	t.Parallel()

	agent := &fakeAgent{responses: []*adk.TypedAgentEvent[*schema.AgenticMessage]{
		adk.EventFromAgenticMessage(assistantMessage("partial"), nil, schema.AgenticRoleTypeAssistant),
	}}
	session := New(context.Background(), agent, "system")
	run, err := session.Start("hello")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if event := run.Next(); event.Kind != EventTextDelta {
		t.Fatalf("first event = %#v", event)
	}
	run.Cancel()
	terminal := run.Next()

	if terminal.Kind != EventCanceled || terminal.Text != "partial" {
		t.Fatalf("terminal event = %#v", terminal)
	}
	if len(session.history) != 2 || session.history[1].Role != schema.AgenticRoleTypeUser {
		t.Fatalf("session history = %#v", session.history)
	}
}

// TestFailedRunDoesNotCommitAssistantHistory 验证运行失败不会污染下一轮助手历史。
func TestFailedRunDoesNotCommitAssistantHistory(t *testing.T) {
	t.Parallel()

	agent := &fakeAgent{responses: []*adk.TypedAgentEvent[*schema.AgenticMessage]{
		adk.EventFromAgenticMessage(assistantMessage("partial"), nil, schema.AgenticRoleTypeAssistant),
		{Err: errors.New("model failed")},
	}}
	session := New(context.Background(), agent, "")
	run, err := session.Start("hello")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	events := receiveAll(run)

	terminal := events[len(events)-1]
	if terminal.Kind != EventFailed || terminal.Text != "partial" || !strings.Contains(terminal.Err.Error(), "model failed") {
		t.Fatalf("terminal event = %#v", terminal)
	}
	if len(session.history) != 1 || session.history[0].Role != schema.AgenticRoleTypeUser {
		t.Fatalf("session history = %#v", session.history)
	}
}

// TestToolEventsAreDeduplicated 验证流式重复 ContentBlock 不会重复报告生命周期。
func TestToolEventsAreDeduplicated(t *testing.T) {
	t.Parallel()

	message := &schema.AgenticMessage{ContentBlocks: []*schema.ContentBlock{
		schema.NewContentBlock(&schema.FunctionToolCall{Name: "calculator", CallID: "call-1"}),
		schema.NewContentBlock(&schema.FunctionToolCall{Name: "calculator", CallID: "call-1"}),
		schema.NewContentBlock(&schema.FunctionToolResult{Name: "calculator", CallID: "call-1"}),
		schema.NewContentBlock(&schema.FunctionToolResult{Name: "calculator", CallID: "call-1"}),
	}}
	agent := &fakeAgent{responses: []*adk.TypedAgentEvent[*schema.AgenticMessage]{
		adk.EventFromAgenticMessage(message, nil, schema.AgenticRoleTypeAssistant),
	}}
	run, err := New(context.Background(), agent, "").Start("calculate")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	events := receiveAll(run)

	if len(events) != 3 || events[0].Kind != EventToolStarted || events[1].Kind != EventToolFinished || events[2].Kind != EventCompleted {
		t.Fatalf("events = %#v", events)
	}
}

// TestSessionRejectsInvalidOrOverlappingRuns 验证会话层自身保护输入和单活动运行约束。
func TestSessionRejectsInvalidOrOverlappingRuns(t *testing.T) {
	t.Parallel()

	if _, err := New(context.Background(), nil, "").Start("hello"); !errors.Is(err, ErrAgentUnavailable) {
		t.Fatalf("nil Agent error = %v", err)
	}
	session := New(context.Background(), &fakeAgent{}, "")
	if _, err := session.Start("   "); !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("empty input error = %v", err)
	}
	run, err := session.Start("first")
	if err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	if _, err := session.Start("second"); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("overlapping Start() error = %v", err)
	}
	if event := run.Next(); event.Kind != EventCompleted {
		t.Fatalf("terminal event = %#v", event)
	}
}

func receiveAll(run Run) []Event {
	events := make([]Event, 0, 4)
	for {
		event := run.Next()
		events = append(events, event)
		switch event.Kind {
		case EventCompleted, EventCanceled, EventFailed:
			return events
		}
	}
}

func assistantText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var text strings.Builder
	for _, block := range message.ContentBlocks {
		if block != nil && block.AssistantGenText != nil {
			text.WriteString(block.AssistantGenText.Text)
		}
	}
	return text.String()
}
