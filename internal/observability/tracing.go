package observability

import (
	"context"
	"fmt"
	"sync"

	"easygo-agent/internal/config"
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

// Tracing contains the run tracer, Eino callback handler, and exporter lifecycle.
type Tracing struct {
	Tracer   trace.Tracer
	Handler  callbacks.Handler
	logger   *zap.Logger
	shutdown func(context.Context) error
	once     sync.Once
	err      error
}

// NewTracing constructs disabled no-op tracing or the optional stdout exporter.
func NewTracing(_ context.Context, cfg config.TracingConfig, logger *zap.Logger) (*Tracing, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if !cfg.Enabled {
		provider := noop.NewTracerProvider()
		return &Tracing{
			Tracer:   provider.Tracer(instrumentationName),
			Handler:  callbacks.NewHandlerBuilder().Build(),
			logger:   logger,
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
		Handler:  newEinoTracingHandler(tracer, logger),
		logger:   logger,
		shutdown: provider.Shutdown,
	}, nil
}

// Shutdown flushes and closes the tracing provider at most once.
func (tracing *Tracing) Shutdown(ctx context.Context) error {
	if tracing == nil {
		return nil
	}
	tracing.once.Do(func() {
		tracing.err = tracing.shutdown(ctx)
		if tracing.err != nil {
			tracing.logger.Error("shutdown tracing failed", zap.Error(tracing.err))
		}
	})
	if tracing.err != nil {
		return fmt.Errorf("shutdown tracing: %w", tracing.err)
	}
	return nil
}

// noopShutdown leaves disabled tracing unchanged.
func noopShutdown(context.Context) error {
	return nil
}

// newEinoTracingHandler creates payload-blind spans for model and Tool callbacks.
func newEinoTracingHandler(tracer trace.Tracer, logger *zap.Logger) callbacks.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	// onStart begins a span only for model and Tool components.
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
	// onEnd finishes a component span without inspecting its output.
	onEnd := func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
		if hasCallbackSpan(ctx) {
			trace.SpanFromContext(ctx).End()
		}
		return ctx
	}
	// onError records and finishes a failed component span.
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

// hasCallbackSpan reports whether this handler started a child span in the context.
func hasCallbackSpan(ctx context.Context) bool {
	started, _ := ctx.Value(callbackSpanContextKey{}).(bool)
	return started
}

// callbackSpanName maps supported Eino components to stable span names.
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
