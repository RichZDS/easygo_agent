package observability

import (
	"context"
	"fmt"
	"sync"

	"easygo-agent/internal/config"
	"easygo-agent/internal/logger"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
)

const instrumentationName = "easygo-agent"

type callbackSpanContextKey struct{}

// Tracing 包含运行 tracer、Eino callback handler 以及导出器生命周期。
type Tracing struct {
	Tracer   trace.Tracer
	Handler  callbacks.Handler
	shutdown func(context.Context) error
	once     sync.Once
	err      error
}

// NewTracing 构造禁用的空 tracing，或可选的 stdout 导出器。
func NewTracing(_ context.Context, cfg config.TracingConfig) (*Tracing, error) {
	if !cfg.Enabled {
		provider := noop.NewTracerProvider()
		return &Tracing{
			Tracer:   provider.Tracer(instrumentationName),
			Handler:  callbacks.NewHandlerBuilder().Build(),
			shutdown: noopShutdown,
		}, nil
	}
	if cfg.Exporter != "stdout" {
		err := fmt.Errorf("unsupported tracing exporter %q", cfg.Exporter)
		logger.Error("create tracing failed", zap.String("exporter", cfg.Exporter), zap.Error(err))
		return nil, err
	}

	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		wrappedErr := fmt.Errorf("create stdout trace exporter: %w", err)
		logger.Error("create tracing failed", zap.String("exporter", cfg.Exporter), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))
	tracer := provider.Tracer(instrumentationName)
	return &Tracing{
		Tracer:   tracer,
		Handler:  newEinoTracingHandler(tracer),
		shutdown: provider.Shutdown,
	}, nil
}

// Shutdown 最多一次地刷新并关闭 tracing provider。
func (tracing *Tracing) Shutdown(ctx context.Context) error {
	if tracing == nil {
		return nil
	}
	// shutdownOnce 保证 tracing 只关闭一次。
	tracing.once.Do(func() {
		tracing.err = tracing.shutdown(ctx)
		if tracing.err != nil {
			logger.Error("shutdown tracing failed", zap.Error(tracing.err))
		}
	})
	if tracing.err != nil {
		return fmt.Errorf("shutdown tracing: %w", tracing.err)
	}
	return nil
}

// noopShutdown 保持禁用 tracing 不变。
func noopShutdown(context.Context) error {
	return nil
}

// newEinoTracingHandler 为模型和 Tool callback 创建不记录载荷的 span。
func newEinoTracingHandler(tracer trace.Tracer) callbacks.Handler {
	// onStart 仅为模型和 Tool 组件开启 span。
	onStart := func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
		spanName, ok := callbackSpanName(info)
		if !ok {
			return ctx
		}
		componentName := ""
		componentType := ""
		if info != nil {
			componentName = info.Name
			componentType = string(info.Component)
		}
		ctx, _ = tracer.Start(ctx, spanName,
			trace.WithAttributes(
				attribute.String("eino.component", componentType),
				attribute.String("eino.name", componentName),
			),
		)
		return context.WithValue(ctx, callbackSpanContextKey{}, true)
	}
	// onEnd 结束组件 span，且不检查输出内容。
	onEnd := func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
		if hasCallbackSpan(ctx) {
			trace.SpanFromContext(ctx).End()
		}
		return ctx
	}
	// onError 记录失败组件 span 并结束它。
	onError := func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
		if hasCallbackSpan(ctx) {
			span := trace.SpanFromContext(ctx)
			span.RecordError(err)
			span.SetStatus(codes.Error, "component failed")
			span.End()
		}
		fields := []zap.Field{zap.Error(err)}
		if info != nil {
			fields = append(fields, zap.String("component", string(info.Component)), zap.String("name", info.Name))
		}
		logger.Error("eino component failed", fields...)
		return ctx
	}
	return callbacks.NewHandlerBuilder().
		OnStartFn(onStart).
		OnEndFn(onEnd).
		OnErrorFn(onError).
		Build()
}

// hasCallbackSpan 判断当前 handler 是否已在 context 中启动子 span。
func hasCallbackSpan(ctx context.Context) bool {
	started, _ := ctx.Value(callbackSpanContextKey{}).(bool)
	return started
}

// callbackSpanName 将支持的 Eino 组件映射为稳定 span 名。
func callbackSpanName(info *callbacks.RunInfo) (string, bool) {
	if info == nil {
		return "", false
	}
	switch info.Component {
	case components.ComponentOfChatModel:
		return "model.generate", true
	case components.ComponentOfTool:
		return "tool.execute", true
	default:
		return "", false
	}
}
