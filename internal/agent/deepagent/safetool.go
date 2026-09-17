package deepagent

import (
	"context"
	"errors"
	"fmt"
	"io"

	"easygo-agent/internal/agent/telemetry"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// safeToolMiddleware 把工具执行错误转成可读结果，让模型继续反思而不是中断整轮运行。
type safeToolMiddleware struct {
	*adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]
}

// newSafeToolMiddleware 构造官方 ch05 SafeTool 模式的 ChatModelAgent middleware。
func newSafeToolMiddleware() adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage] {
	return &safeToolMiddleware{
		TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]{},
	}
}

var _ adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage] = (*safeToolMiddleware)(nil)

// WrapInvokableToolCall 拦截同步工具调用，把可恢复错误写成 tool result。
func (middleware *safeToolMiddleware) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	toolContext *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	name := toolName(toolContext)
	// invokeWithRecoverableError 把可恢复的工具错误转成结果文本。
	invokeWithRecoverableError := func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		ctx, span := telemetry.Start(ctx, "tool", name, zap.String("call_id", compose.GetToolCallID(ctx)))
		result, err := endpoint(ctx, args, opts...)
		status := ""
		if err != nil && !isFatalToolError(err) {
			status = "recovered"
		}
		span.Finish(status, err, zap.Int("result_bytes", len(result)))
		if converted, keep := recoverToolError(ctx, name, err); keep {
			telemetry.Logger(ctx).Error("tool invocation failed", zap.String("tool", name), zap.Error(err))
			return "", err
		} else if converted != "" {
			return converted, nil
		}
		return result, nil
	}
	return invokeWithRecoverableError, nil
}

// WrapStreamableToolCall 拦截流式工具调用，把启动失败或流内错误写成结果文本。
func (middleware *safeToolMiddleware) WrapStreamableToolCall(
	_ context.Context,
	endpoint adk.StreamableToolCallEndpoint,
	toolContext *adk.ToolContext,
) (adk.StreamableToolCallEndpoint, error) {
	name := toolName(toolContext)
	// streamWithRecoverableError 把可恢复的流式工具错误转成单 chunk 结果。
	streamWithRecoverableError := func(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		ctx, span := telemetry.Start(ctx, "tool", name, zap.String("call_id", compose.GetToolCallID(ctx)), zap.Bool("streaming", true))
		reader, err := endpoint(ctx, args, opts...)
		if reader == nil && err == nil {
			err = errors.New("tool returned a nil stream")
		}
		if converted, keep := recoverToolError(ctx, name, err); keep {
			span.Finish("", err)
			return nil, err
		} else if converted != "" {
			span.Finish("recovered", err)
			return singleChunkReader(converted), nil
		}
		return wrapStreamReader(ctx, reader, span), nil
	}
	return streamWithRecoverableError, nil
}

// unknownToolResult 把模型幻觉出的未知工具名写成结果文本。
func unknownToolResult(ctx context.Context, name, input string) (string, error) {
	err := fmt.Errorf("unknown tool %q", name)
	_, span := telemetry.Start(ctx, "tool", name, zap.String("call_id", compose.GetToolCallID(ctx)))
	span.Finish("recovered", err)
	return formatToolError(err), nil
}

// recoverToolError 判断错误应上抛还是转成结果文本。converted 非空表示已转换。
func recoverToolError(ctx context.Context, name string, err error) (converted string, keep bool) {
	if err == nil {
		return "", false
	}
	if isFatalToolError(err) {
		return "", true
	}
	telemetry.Logger(ctx).Error("convert tool error to result", zap.String("tool", name), zap.Error(err))
	return formatToolError(err), false
}

// isFatalToolError 判断工具错误是否必须中断 Agent，例如 interrupt 与取消。
func isFatalToolError(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := compose.IsInterruptRerunError(err); ok {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// formatToolError 按官方 SafeTool 格式把错误写成模型可读文本。
func formatToolError(err error) string {
	return fmt.Sprintf("[tool error] %v", err)
}

// toolName 从 ToolContext 取出工具名，缺失时用 unknown。
func toolName(toolContext *adk.ToolContext) string {
	if toolContext == nil || toolContext.Name == "" {
		return "unknown"
	}
	return toolContext.Name
}

// singleChunkReader 返回只发出一条文本后结束的流。
func singleChunkReader(message string) *schema.StreamReader[string] {
	reader, writer := schema.Pipe[string](1)
	_ = writer.Send(message, nil)
	writer.Close()
	return reader
}

// wrapStreamReader 把流内错误改写成最后一条错误文本，避免管道失败。
func wrapStreamReader(ctx context.Context, source *schema.StreamReader[string], span *telemetry.Span) *schema.StreamReader[string] {
	reader, writer := schema.Pipe[string](64)
	stop := context.AfterFunc(ctx, func() { span.Finish("", ctx.Err()) })
	// copyStreamChunks 转发成功 chunk，并把流内错误写成结果文本。
	go func() {
		defer writer.Close()
		defer stop()
		defer func() { span.Finish("", ctx.Err()) }()
		defer source.Close()
		for {
			chunk, err := source.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				if isFatalToolError(err) {
					span.Finish("", err)
					_ = writer.Send("", err)
					return
				}
				span.Finish("recovered", err)
				_ = writer.Send(formatToolError(err), nil)
				return
			}
			if writer.Send(chunk, nil) {
				span.Finish("canceled", io.ErrClosedPipe)
				return
			}
		}
	}()
	return reader
}
