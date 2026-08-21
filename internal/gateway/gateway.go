package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	callbacktemplate "github.com/cloudwego/eino/utils/callbacks"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
)

// Gateway owns Eino ReAct execution behind the stable Runner interface.
type Gateway struct {
	agent        *react.Agent
	tracer       trace.Tracer
	traceHandler callbacks.Handler
	logger       *zap.Logger
}

// New constructs one concurrency-safe Eino ReAct Gateway.
func New(
	ctx context.Context,
	cfg Config,
	chatModel model.ToolCallingChatModel,
	tools []tool.BaseTool,
	tracer trace.Tracer,
	traceHandler callbacks.Handler,
	logger *zap.Logger,
) (*Gateway, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
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
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("easygo-agent")
	}
	if traceHandler == nil {
		traceHandler = callbacks.NewHandlerBuilder().Build()
	}

	reactAgent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: tools,
		},
		MaxStep:       cfg.MaxSteps,
		GraphName:     "TemplateReActAgent",
		ModelNodeName: "ChatModel",
		ToolsNodeName: "Tools",
	})
	if err != nil {
		wrappedErr := fmt.Errorf("create Eino ReAct Agent: %w", err)
		logger.Error("create Gateway failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return &Gateway{
		agent:        reactAgent,
		tracer:       tracer,
		traceHandler: traceHandler,
		logger:       logger,
	}, nil
}

// Run validates one request and starts its independent event stream.
func (gateway *Gateway) Run(ctx context.Context, request Request) (EventStream, error) {
	messages, err := gateway.convertMessages(request.Messages)
	if err != nil {
		wrappedErr := fmt.Errorf("validate Gateway request: %w", err)
		gateway.logger.Error("start Gateway run failed", zap.String("stage", "request"), zap.Error(wrappedErr))
		return nil, wrappedErr
	}

	runCtx, cancel := context.WithCancel(ctx)
	runCtx, rootSpan := gateway.tracer.Start(runCtx, "agent.run")
	stream := newEventStream(cancel, gateway.logger)
	toolHandler := gateway.newToolEventHandler(stream)
	handlers := []callbacks.Handler{toolHandler, gateway.traceHandler}

	// executeRun owns Eino startup and streaming until all resources are released.
	go func() {
		terminal := gateway.executeRun(runCtx, messages, stream, handlers)
		if terminal.Kind == EventFailed {
			rootSpan.RecordError(terminal.Err)
			rootSpan.SetStatus(codes.Error, "run failed")
		}
		rootSpan.End()
		cancel()
		stream.finish(terminal)
	}()
	return stream, nil
}

// executeRun starts Eino asynchronously so the caller can cancel before the first model chunk.
func (gateway *Gateway) executeRun(ctx context.Context, messages []*schema.Message, stream *eventStream, handlers []callbacks.Handler) Event {
	einoStream, err := gateway.agent.Stream(
		ctx,
		messages,
		agent.WithComposeOptions(compose.WithCallbacks(handlers...)),
	)
	if err != nil {
		if ctx.Err() != nil {
			return Event{Kind: EventCanceled}
		}
		wrappedErr := fmt.Errorf("start Eino ReAct stream: %w", err)
		gateway.logger.Error("start Gateway run failed", zap.String("stage", "react_stream"), zap.Error(wrappedErr))
		return Event{Kind: EventFailed, Err: wrappedErr}
	}
	defer einoStream.Close()
	return gateway.consumeRun(ctx, einoStream, stream)
}

// consumeRun projects Eino message chunks into stable Gateway events.
func (gateway *Gateway) consumeRun(ctx context.Context, einoStream *schema.StreamReader[*schema.Message], stream *eventStream) Event {
	var content strings.Builder
	for {
		message, err := einoStream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return Event{Kind: EventCanceled}
			}
			if errors.Is(err, io.EOF) {
				return Event{Kind: EventCompleted, Text: content.String()}
			}
			wrappedErr := fmt.Errorf("receive Eino ReAct stream: %w", err)
			gateway.logger.Error("consume Gateway run failed", zap.String("stage", "stream_receive"), zap.Error(wrappedErr))
			return Event{Kind: EventFailed, Err: wrappedErr}
		}
		if message == nil || message.Content == "" {
			continue
		}
		content.WriteString(message.Content)
		stream.emit(Event{Kind: EventTextDelta, Text: message.Content})
	}
}

// convertMessages validates stable roles and converts them to Eino messages.
func (gateway *Gateway) convertMessages(messages []Message) ([]*schema.Message, error) {
	if len(messages) == 0 {
		err := errors.New("request messages cannot be empty")
		gateway.logger.Error("convert Gateway messages failed", zap.Error(err))
		return nil, err
	}
	converted := make([]*schema.Message, 0, len(messages))
	for index, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			err := fmt.Errorf("message %d content cannot be empty", index)
			gateway.logger.Error("convert Gateway messages failed", zap.Int("message_index", index), zap.Error(err))
			return nil, err
		}
		switch message.Role {
		case RoleSystem:
			converted = append(converted, schema.SystemMessage(content))
		case RoleUser:
			converted = append(converted, schema.UserMessage(content))
		case RoleAssistant:
			converted = append(converted, schema.AssistantMessage(content, nil))
		default:
			err := fmt.Errorf("message %d has unsupported role %q", index, message.Role)
			gateway.logger.Error("convert Gateway messages failed", zap.Int("message_index", index), zap.Error(err))
			return nil, err
		}
	}
	return converted, nil
}

// newToolEventHandler projects Tool lifecycle callbacks without payload data.
func (gateway *Gateway) newToolEventHandler(stream *eventStream) callbacks.Handler {
	// onToolStart emits a Tool start event without arguments.
	onToolStart := func(ctx context.Context, info *callbacks.RunInfo, _ *tool.CallbackInput) context.Context {
		stream.emit(Event{Kind: EventToolStart, ToolName: callbackName(info)})
		return ctx
	}
	// onToolEnd emits a Tool completion event without output.
	onToolEnd := func(ctx context.Context, info *callbacks.RunInfo, _ *tool.CallbackOutput) context.Context {
		stream.emit(Event{Kind: EventToolEnd, ToolName: callbackName(info)})
		return ctx
	}
	// onToolError emits a Tool completion event carrying only the error.
	onToolError := func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
		toolName := callbackName(info)
		gateway.logger.Error("Eino Tool failed", zap.String("tool", toolName), zap.Error(err))
		stream.emit(Event{Kind: EventToolEnd, ToolName: toolName, Err: err})
		return ctx
	}
	return react.BuildAgentCallback(nil, &callbacktemplate.ToolCallbackHandler{
		OnStart: onToolStart,
		OnEnd:   onToolEnd,
		OnError: onToolError,
	})
}

// callbackName safely extracts a callback component name.
func callbackName(info *callbacks.RunInfo) string {
	if info == nil {
		return ""
	}
	return info.Name
}

var _ Runner = (*Gateway)(nil)
