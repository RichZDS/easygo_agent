package observability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"easygo-agent/internal/config"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestEinoCallbackCreatesSafeSpans 验证模型和 Tool callback 会创建不含载荷的 span。
func TestEinoCallbackCreatesSafeSpans(t *testing.T) {
	t.Parallel()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	handler := newEinoTracingHandler(provider.Tracer(instrumentationName))

	modelInfo := &callbacks.RunInfo{Name: "test-model", Component: components.ComponentOfChatModel}
	modelContext := handler.OnStart(context.Background(), modelInfo, "prompt text")
	handler.OnEnd(modelContext, modelInfo, "response text")

	toolInfo := &callbacks.RunInfo{Name: "calculator", Component: components.ComponentOfTool}
	toolContext := handler.OnStart(context.Background(), toolInfo, `{"secret-key":"value"}`)
	handler.OnEnd(toolContext, toolInfo, `{"result":3}`)

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(spans))
	}
	if spans[0].Name() != "model.generate" || spans[1].Name() != "tool.execute" {
		t.Fatalf("span names = %q, %q", spans[0].Name(), spans[1].Name())
	}
	for _, span := range spans {
		attributes := fmt.Sprint(span.Attributes())
		for _, secret := range []string{"secret-key", "prompt text", "response text", "result"} {
			if strings.Contains(attributes, secret) {
				t.Errorf("span attributes contain sensitive value %q: %s", secret, attributes)
			}
		}
	}
}

// TestEinoCallbackRecordsErrors 验证 callback 失败会设置 OTel error 状态。
func TestEinoCallbackRecordsErrors(t *testing.T) {
	t.Parallel()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	handler := newEinoTracingHandler(provider.Tracer(instrumentationName))
	info := &callbacks.RunInfo{Name: "calculator", Component: components.ComponentOfTool}
	spanContext := handler.OnStart(context.Background(), info, nil)
	handler.OnError(spanContext, info, errors.New("tool failed"))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Status().Code != codes.Error {
		t.Errorf("span status = %v, want error", spans[0].Status().Code)
	}
}

// TestEinoCallbackIgnoresUnsupportedComponents 验证通用 callback 不能结束父 span。
func TestEinoCallbackIgnoresUnsupportedComponents(t *testing.T) {
	t.Parallel()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := provider.Tracer(instrumentationName)
	parentContext, parentSpan := tracer.Start(context.Background(), "agent.run")
	handler := newEinoTracingHandler(tracer)
	info := &callbacks.RunInfo{Name: "loader", Component: components.ComponentOfLoader}
	callbackContext := handler.OnStart(parentContext, info, nil)
	handler.OnEnd(callbackContext, info, nil)

	if spans := recorder.Ended(); len(spans) != 0 {
		t.Fatalf("callback ended %d spans before parent shutdown", len(spans))
	}
	parentSpan.End()
	if spans := recorder.Ended(); len(spans) != 1 || spans[0].Name() != "agent.run" {
		t.Fatalf("ended spans = %#v", spans)
	}
}

// TestNewTracingDisabled 验证禁用 tracing 安全且可幂等关闭。
func TestNewTracingDisabled(t *testing.T) {
	t.Parallel()

	tracing, err := NewTracing(context.Background(), config.TracingConfig{Enabled: false, Exporter: "stdout"})
	if err != nil {
		t.Fatalf("NewTracing() error = %v", err)
	}
	if tracing.Tracer == nil || tracing.Handler == nil {
		t.Fatal("disabled tracing returned nil dependencies")
	}
	if err := tracing.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown() error = %v", err)
	}
	if err := tracing.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
}

// TestNewTracingRejectsUnsupportedExporter 验证不接受厂商专用导出器。
func TestNewTracingRejectsUnsupportedExporter(t *testing.T) {
	t.Parallel()

	_, err := NewTracing(context.Background(), config.TracingConfig{Enabled: true, Exporter: "vendor"})
	if err == nil {
		t.Fatal("NewTracing() error = nil, want unsupported exporter error")
	}
}
