// Package telemetry records a run's execution phases without retaining prompts,
// tool payloads or stream chunks. Context carries correlation, never model input.
package telemetry

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type Identity struct {
	SessionID string
	RunID     string
	WorkerID  string
}

type traceKey struct{}
type spanKey struct{}
type loggerKey struct{}

type trace struct {
	mu           sync.Mutex
	log          *zap.Logger
	sequence     uint64
	spans        uint64
	models       int
	tools        int
	compressions int
}

// Span owns one phase. Finish is safe to repeat or race with cancellation.
type Span struct {
	trace   *trace
	id      uint64
	parent  uint64
	phase   string
	name    string
	started time.Time
	fields  []zap.Field
	once    sync.Once
}

// WithLogger selects a local sink (for example a Zap observer in tests).
func WithLogger(ctx context.Context, log *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, log)
}

// StartRun starts a new execution even when a recovered run reuses its run ID.
func StartRun(ctx context.Context, identity Identity, fields ...zap.Field) (context.Context, *Span) {
	log := Logger(ctx).With(zap.String("session_id", identity.SessionID), zap.String("run_id", identity.RunID),
		zap.String("execution_id", uuid.NewString()), zap.String("worker_id", identity.WorkerID))
	ctx = context.WithValue(ctx, traceKey{}, &trace{log: log})
	ctx = context.WithValue(ctx, spanKey{}, (*Span)(nil))
	return Start(ctx, "run", "agent", fields...)
}

// Start opens a phase under the current span. Without a run it is a no-op, so
// models can also be used by standalone evals or the daily memory job.
func Start(ctx context.Context, phase, name string, fields ...zap.Field) (context.Context, *Span) {
	t, _ := ctx.Value(traceKey{}).(*trace)
	if t == nil {
		return ctx, nil
	}
	s := &Span{trace: t, phase: phase, name: name, started: time.Now(), fields: append([]zap.Field(nil), fields...)}
	if parent, _ := ctx.Value(spanKey{}).(*Span); parent != nil {
		s.parent = parent.id
	}
	t.mu.Lock()
	t.spans++
	s.id = t.spans
	switch phase {
	case "model":
		t.models++
	case "tool":
		t.tools++
	case "compression":
		t.compressions++
	}
	s.emitLocked("started", nil)
	t.mu.Unlock()
	return context.WithValue(ctx, spanKey{}, s), s
}

func (s *Span) Finish(status string, err error, fields ...zap.Field) {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if status == "" {
			status = "completed"
			if err != nil {
				status = "failed"
			}
			if errors.Is(err, context.Canceled) {
				status = "canceled"
			}
		}
		fields = append(fields, zap.String("status", status), zap.Float64("duration_ms", float64(time.Since(s.started))/float64(time.Millisecond)))
		if err != nil {
			fields = append(fields, zap.Error(err))
		}
		s.trace.mu.Lock()
		defer s.trace.mu.Unlock()
		if s.phase == "run" {
			fields = append(fields, zap.Int("model_calls", s.trace.models), zap.Int("tool_calls", s.trace.tools), zap.Int("compressions", s.trace.compressions))
		}
		s.emitLocked("finished", fields)
	})
}

func (s *Span) emitLocked(event string, fields []zap.Field) {
	s.trace.sequence++
	base := []zap.Field{zap.Uint64("sequence", s.trace.sequence), zap.Uint64("span_id", s.id),
		zap.Uint64("parent_span_id", s.parent), zap.String("phase", s.phase), zap.String("name", s.name), zap.String("event", event)}
	base = append(base, s.fields...)
	s.trace.log.Info("agent.phase", append(base, fields...)...)
}

// Logger attaches run/span correlation to existing debug and diagnostic logs.
func Logger(ctx context.Context) *zap.Logger {
	if ctx != nil {
		if t, _ := ctx.Value(traceKey{}).(*trace); t != nil {
			if s, _ := ctx.Value(spanKey{}).(*Span); s != nil {
				return t.log.With(zap.Uint64("span_id", s.id))
			}
			return t.log
		}
		if log, _ := ctx.Value(loggerKey{}).(*zap.Logger); log != nil {
			return log
		}
	}
	return zap.L()
}
