package gateway

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	templatetools "easygo-agent/internal/tools"
	"github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
)

// TestGatewayRunsReActWithCalculator 通过真实 Eino ReAct 循环校验对外事件流。
func TestGatewayRunsReActWithCalculator(t *testing.T) {
	t.Parallel()

	calculator, err := templatetools.NewCalculator(zap.NewNop())
	if err != nil {
		t.Fatalf("NewCalculator() error = %v", err)
	}
	agentGateway, err := New(
		context.Background(),
		Config{MaxSteps: 8},
		&fakeToolCallingModel{},
		[]einotool.BaseTool{calculator},
		noop.NewTracerProvider().Tracer("test"),
		callbacks.NewHandlerBuilder().Build(),
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	stream, err := agentGateway.Run(context.Background(), Request{Messages: []Message{
		{Role: RoleSystem, Content: "Use tools when helpful."},
		{Role: RoleUser, Content: "What is 2 + 3?"},
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	defer closeStream(t, stream)

	events := receiveAll(t, stream)
	wantKinds := []EventKind{EventToolStart, EventToolEnd, EventTextDelta, EventTextDelta, EventCompleted}
	if len(events) != len(wantKinds) {
		t.Fatalf("events = %#v, want %d events", events, len(wantKinds))
	}
	for index, wantKind := range wantKinds {
		if events[index].Kind != wantKind {
			t.Errorf("events[%d].Kind = %q, want %q", index, events[index].Kind, wantKind)
		}
	}
	if events[0].ToolName != "calculator" || events[1].ToolName != "calculator" {
		t.Errorf("Tool events = %#v, %#v", events[0], events[1])
	}
	if got := events[len(events)-1].Text; got != "result is 5" {
		t.Errorf("Completed.Text = %q, want %q", got, "result is 5")
	}
}

// TestGatewayRejectsInvalidRequests 验证稳定请求校验发生在模型执行之前。
func TestGatewayRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	agentGateway := newTestGateway(t, &fakeToolCallingModel{})
	tests := []Request{
		{},
		{Messages: []Message{{Role: Role("tool"), Content: "hidden"}}},
		{Messages: []Message{{Role: RoleUser, Content: "   "}}},
	}
	for _, request := range tests {
		if _, err := agentGateway.Run(context.Background(), request); err == nil {
			t.Errorf("Run(%#v) error = nil, want validation error", request)
		}
	}
}

// TestGatewayCancellationProducesCanceled 验证调用方取消不会被报告为失败。
func TestGatewayCancellationProducesCanceled(t *testing.T) {
	t.Parallel()

	agentGateway := newTestGateway(t, &blockingToolCallingModel{})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := agentGateway.Run(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "wait"}}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	cancel()
	defer closeStream(t, stream)

	events := receiveAll(t, stream)
	if len(events) != 1 || events[0].Kind != EventCanceled {
		t.Fatalf("events = %#v, want one canceled event", events)
	}
}

// newTestGateway 使用确定性空基础设施创建 Gateway。
func newTestGateway(t *testing.T, chatModel einomodel.ToolCallingChatModel) *Gateway {
	t.Helper()

	calculator, err := templatetools.NewCalculator(zap.NewNop())
	if err != nil {
		t.Fatalf("NewCalculator() error = %v", err)
	}
	agentGateway, err := New(
		context.Background(),
		Config{MaxSteps: 8},
		chatModel,
		[]einotool.BaseTool{calculator},
		noop.NewTracerProvider().Tracer("test"),
		callbacks.NewHandlerBuilder().Build(),
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return agentGateway
}

// receiveAll 消费对外事件直到稳定的 EOF 标记。
func receiveAll(t *testing.T, stream EventStream) []Event {
	t.Helper()

	done := make(chan []Event, 1)
	// receive 在测试 goroutine 外读取流，以便超时检测卡住。
	go func() {
		var events []Event
		for {
			event, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				done <- events
				return
			}
			if err != nil {
				t.Errorf("Recv() error = %v", err)
				done <- events
				return
			}
			events = append(events, event)
		}
	}()

	select {
	case events := <-done:
		return events
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Gateway stream")
		return nil
	}
}

// closeStream 关闭 Gateway 流并报告清理错误。
func closeStream(t *testing.T, stream EventStream) {
	t.Helper()

	if err := stream.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

type fakeToolCallingModel struct {
	tools []*schema.ToolInfo
}

// WithTools 返回绑定到请求 Tool 定义的不可变假模型。
func (model *fakeToolCallingModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return &fakeToolCallingModel{tools: append([]*schema.ToolInfo(nil), tools...)}, nil
}

// Generate 返回确定性的非流式等价响应。
func (model *fakeToolCallingModel) Generate(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if hasToolResult(input) {
		return schema.AssistantMessage("result is 5", nil), nil
	}
	return calculatorToolCall(), nil
}

// Stream 先请求 Calculator，再发出确定性最终响应。
func (model *fakeToolCallingModel) Stream(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if hasToolResult(input) {
		return schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("result ", nil),
			schema.AssistantMessage("is 5", nil),
		}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{calculatorToolCall()}), nil
}

// calculatorToolCall 返回假模型的确定性 Tool 请求。
func calculatorToolCall() *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:   "call-1",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      "calculator",
			Arguments: `{"operation":"add","a":2,"b":3}`,
		},
	}})
}

// hasToolResult 判断 ReAct 是否已向模型提供 Tool 结果。
func hasToolResult(messages []*schema.Message) bool {
	for _, message := range messages {
		if message.Role == schema.Tool {
			return true
		}
	}
	return false
}

type blockingToolCallingModel struct{}

// WithTools 原样返回取消测试使用的模型。
func (model *blockingToolCallingModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return model, nil
}

// Generate 阻塞直到 context 被取消。
func (model *blockingToolCallingModel) Generate(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Stream 返回仅在 context 取消时结束的流。
func (model *blockingToolCallingModel) Stream(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](1)
	// waitForCancellation 把 context 取消转发到 Eino 流。
	go func() {
		<-ctx.Done()
		writer.Send(nil, ctx.Err())
		writer.Close()
	}()
	return reader, nil
}
