package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"easygo-agent/internal/logger"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	callbacktemplate "github.com/cloudwego/eino/utils/callbacks"
	"go.uber.org/zap"
)

// Gateway 在稳定 Runner 接口后面持有 Eino ReAct 执行。
type Gateway struct {
	agent adk.TypedAgent[*schema.Message]
}

// New 构造一个并发安全的 Eino ReAct Gateway。
func New(
	ctx context.Context,
	cfg Config,
	chatModel model.ToolCallingChatModel,
	tools []tool.BaseTool,
) (*Gateway, error) {
	if cfg.MaxSteps <= 0 {
		err := errors.New("Gateway max steps must be greater than zero")
		logger.Error("create Gateway failed", zap.String("field", "max_steps"), zap.Error(err))
		return nil, err
	}
	if chatModel == nil {
		err := errors.New("Gateway chat model cannot be nil")
		logger.Error("create Gateway failed", zap.String("field", "chat_model"), zap.Error(err))
		return nil, err
	}

	// reactAgent, err := react.NewAgent(ctx, &react.AgentConfig{
	// 	ToolCallingModel: chatModel,
	// 	ToolsConfig: compose.ToolsNodeConfig{
	// 		Tools: tools,
	// 	},
	// 	MaxStep:       cfg.MaxSteps,
	// 	GraphName:     "TemplateReActAgent",
	// 	ModelNodeName: "ChatModel",
	// 	ToolsNodeName: "Tools",
	// })

	agent, err := deep.New(ctx, &deep.Config{
		Name:         "deep-agent",
		ChatModel:    chatModel,
		SubAgents:    []adk.Agent{},
		MaxIteration: 100,
	})
	if err != nil {
		wrappedErr := fmt.Errorf("create Eino ReAct Agent: %w", err)
		logger.Error("create Gateway failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return &Gateway{agent: agent}, nil
}

// Run 校验一次请求并启动其独立事件流。
func (gateway *Gateway) Run(ctx context.Context, request Request) (EventStream, error) {
	if err := validateMessages(request.Messages); err != nil {
		wrappedErr := fmt.Errorf("validate Gateway request: %w", err)
		logger.Error("start Gateway run failed", zap.String("stage", "request"), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	request.EnableStreaming = true

	runCtx, cancel := context.WithCancel(ctx)
	stream := newEventStream(cancel)
	toolHandler := gateway.newToolEventHandler(stream)

	// executeRun 负责 Eino 启动与流式读取，直到资源全部释放。
	go func() {
		terminal := gateway.executeRun(runCtx, &request, stream, []callbacks.Handler{toolHandler})
		cancel()
		stream.finish(terminal)
	}()
	return stream, nil
}

// executeRun 异步启动 Eino ADK，以便调用方可在首个模型分片前取消。
func (gateway *Gateway) executeRun(ctx context.Context, input *adk.AgentInput, stream *eventStream, handlers []callbacks.Handler) Event {
	iter := gateway.agent.Run(ctx, input, adk.WithCallbacks(handlers...))
	return gateway.consumeRun(ctx, iter, stream)
}

// consumeRun 将 Eino ADK 事件投影为稳定 Gateway 事件。
func (gateway *Gateway) consumeRun(ctx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent], stream *eventStream) Event {
	var content strings.Builder
	for {
		event, ok := iter.Next()
		if !ok {
			return Event{Kind: EventCompleted, Text: content.String()}
		}
		if ctx.Err() != nil {
			return Event{Kind: EventCanceled}
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			if ctx.Err() != nil {
				return Event{Kind: EventCanceled}
			}
			wrappedErr := fmt.Errorf("receive Eino ADK stream: %w", event.Err)
			logger.Error("consume Gateway run failed", zap.String("stage", "stream_receive"), zap.Error(wrappedErr))
			return Event{Kind: EventFailed, Err: wrappedErr}
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		if terminal := gateway.consumeMessageOutput(ctx, event.Output.MessageOutput, stream, &content); terminal != nil {
			return *terminal
		}
	}
}

// consumeMessageOutput 只把助手文本投影到 Gateway 流；非助手输出直接丢弃。
func (gateway *Gateway) consumeMessageOutput(ctx context.Context, output *adk.MessageVariant, stream *eventStream, content *strings.Builder) *Event {
	if output.IsStreaming && output.MessageStream != nil {
		output.MessageStream.SetAutomaticClose()
		if output.Role != schema.Assistant {
			output.MessageStream.Close()
			return nil
		}
		return gateway.consumeMessageStream(ctx, output.MessageStream, stream, content)
	}
	if output.Role != schema.Assistant || output.Message == nil || output.Message.Content == "" {
		return nil
	}
	content.WriteString(output.Message.Content)
	stream.emit(Event{Kind: EventTextDelta, Text: output.Message.Content})
	return nil
}

// consumeMessageStream 将一条助手消息流的分片投影为 text_delta。
func (gateway *Gateway) consumeMessageStream(ctx context.Context, einoStream *schema.StreamReader[*schema.Message], stream *eventStream, content *strings.Builder) *Event {
	defer einoStream.Close()
	for {
		message, err := einoStream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return &Event{Kind: EventCanceled}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			wrappedErr := fmt.Errorf("receive Eino ADK stream: %w", err)
			logger.Error("consume Gateway run failed", zap.String("stage", "stream_receive"), zap.Error(wrappedErr))
			return &Event{Kind: EventFailed, Err: wrappedErr}
		}
		if message == nil || message.Content == "" {
			continue
		}
		content.WriteString(message.Content)
		stream.emit(Event{Kind: EventTextDelta, Text: message.Content})
	}
}

// validateMessages 校验 Eino 原生消息，保证 Gateway 只接受完整文本对话。
func validateMessages(messages []*schema.Message) error {
	if len(messages) == 0 {
		err := errors.New("request messages cannot be empty")
		logger.Error("validate Gateway messages failed", zap.Error(err))
		return err
	}
	for index, message := range messages {
		if message == nil {
			err := fmt.Errorf("message %d cannot be nil", index)
			logger.Error("validate Gateway messages failed", zap.Int("message_index", index), zap.Error(err))
			return err
		}
		if strings.TrimSpace(message.Content) == "" {
			err := fmt.Errorf("message %d content cannot be empty", index)
			logger.Error("validate Gateway messages failed", zap.Int("message_index", index), zap.Error(err))
			return err
		}
		switch message.Role {
		case schema.System, schema.User, schema.Assistant:
		default:
			err := fmt.Errorf("message %d has unsupported role %q", index, message.Role)
			logger.Error("validate Gateway messages failed", zap.Int("message_index", index), zap.Error(err))
			return err
		}
	}
	return nil
}

// newToolEventHandler 投影 Tool 生命周期 callback，且不携带载荷。
func (gateway *Gateway) newToolEventHandler(stream *eventStream) callbacks.Handler {
	// onToolStart 发出不含参数的 Tool 开始事件。
	onToolStart := func(ctx context.Context, info *callbacks.RunInfo, _ *tool.CallbackInput) context.Context {
		stream.emit(Event{Kind: EventToolStart, ToolName: callbackName(info)})
		return ctx
	}
	// onToolEnd 发出不含输出的 Tool 完成事件。
	onToolEnd := func(ctx context.Context, info *callbacks.RunInfo, _ *tool.CallbackOutput) context.Context {
		stream.emit(Event{Kind: EventToolEnd, ToolName: callbackName(info)})
		return ctx
	}
	// onToolError 发出只携带错误的 Tool 完成事件。
	onToolError := func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
		toolName := callbackName(info)
		logger.Error("Eino Tool failed", zap.String("tool", toolName), zap.Error(err))
		stream.emit(Event{Kind: EventToolEnd, ToolName: toolName, Err: err})
		return ctx
	}
	return react.BuildAgentCallback(nil, &callbacktemplate.ToolCallbackHandler{
		OnStart: onToolStart,
		OnEnd:   onToolEnd,
		OnError: onToolError,
	})
}

// callbackName 安全提取 callback 组件名称。
func callbackName(info *callbacks.RunInfo) string {
	if info == nil {
		return ""
	}
	return info.Name
}

var _ Runner = (*Gateway)(nil)
