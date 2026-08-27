package deepagent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// TestSafeToolMiddlewareConvertsInvokableErrorToResult 验证工具调用错误会变成结果文本而不是中断 Agent。
func TestSafeToolMiddlewareConvertsInvokableErrorToResult(t *testing.T) {
	middleware := newSafeToolMiddleware()
	// failingInvoke 模拟一次返回业务错误的工具调用。
	failingInvoke := func(context.Context, string, ...tool.Option) (string, error) {
		return "", errors.New("cannot divide by zero")
	}

	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), failingInvoke, &adk.ToolContext{Name: "calculator", CallID: "c1"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}

	result, err := wrapped(context.Background(), `{"operation":"divide","a":45,"b":0}`)
	if err != nil {
		t.Fatalf("wrapped invoke returned error %v, want nil so the agent can continue", err)
	}
	if !strings.Contains(result, "[tool error]") || !strings.Contains(result, "cannot divide by zero") {
		t.Fatalf("result = %q, want [tool error] containing cannot divide by zero", result)
	}
}

// TestSafeToolMiddlewarePropagatesInterrupt 验证 interrupt 错误不会被转成 tool result。
func TestSafeToolMiddlewarePropagatesInterrupt(t *testing.T) {
	middleware := newSafeToolMiddleware()
	interruptErr := compose.InterruptAndRerun
	// interruptingInvoke 模拟需要 rerun 的 interrupt。
	interruptingInvoke := func(context.Context, string, ...tool.Option) (string, error) {
		return "", interruptErr
	}

	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), interruptingInvoke, &adk.ToolContext{Name: "calculator"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}

	_, err = wrapped(context.Background(), "{}")
	if !errors.Is(err, interruptErr) {
		t.Fatalf("err = %v, want interrupt error", err)
	}
}

// TestSafeToolMiddlewarePropagatesCanceled 验证取消错误继续向上传播。
func TestSafeToolMiddlewarePropagatesCanceled(t *testing.T) {
	middleware := newSafeToolMiddleware()
	// canceledInvoke 模拟用户取消。
	canceledInvoke := func(context.Context, string, ...tool.Option) (string, error) {
		return "", context.Canceled
	}

	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), canceledInvoke, &adk.ToolContext{Name: "calculator"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}

	_, err = wrapped(context.Background(), "{}")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestSafeToolMiddlewareConvertsStreamableErrorToResult 验证流式工具错误也会变成可读结果。
func TestSafeToolMiddlewareConvertsStreamableErrorToResult(t *testing.T) {
	middleware := newSafeToolMiddleware()
	// failingStream 模拟流式工具在启动时失败。
	failingStream := func(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
		return nil, errors.New("cannot divide by zero")
	}

	wrapped, err := middleware.WrapStreamableToolCall(context.Background(), failingStream, &adk.ToolContext{Name: "calculator"})
	if err != nil {
		t.Fatalf("WrapStreamableToolCall returned error: %v", err)
	}

	reader, err := wrapped(context.Background(), "{}")
	if err != nil {
		t.Fatalf("wrapped stream returned error %v, want nil", err)
	}
	defer reader.Close()

	chunk, err := reader.Recv()
	if err != nil {
		t.Fatalf("recv stream chunk: %v", err)
	}
	if !strings.Contains(chunk, "[tool error]") || !strings.Contains(chunk, "cannot divide by zero") {
		t.Fatalf("chunk = %q, want [tool error] containing cannot divide by zero", chunk)
	}
	if _, err := reader.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("second recv err = %v, want EOF", err)
	}
}

// TestUnknownToolResultReturnsErrorText 验证幻觉工具名会变成结果文本而不是节点错误。
func TestUnknownToolResultReturnsErrorText(t *testing.T) {
	result, err := unknownToolResult(context.Background(), "not_a_tool", `{}`)
	if err != nil {
		t.Fatalf("unknownToolResult returned error %v, want nil", err)
	}
	if !strings.Contains(result, "[tool error]") || !strings.Contains(result, "not_a_tool") {
		t.Fatalf("result = %q, want [tool error] mentioning not_a_tool", result)
	}
}
