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
type phaseSinkKey struct{}

// PhaseEvent is the durable projection of one agent.phase log record.
// Prompt text, tool arguments, tool results, and stream chunks are not fields.
type PhaseEvent struct {
	RunID        string
	ExecutionID  string
	Sequence     uint64
	SpanID       uint64
	ParentSpanID uint64
	Phase        string
	Name         string
	Event        string
	Status       string
	DurationMS   float64
	HasDuration  bool
	Error        string
}

// PhaseSink receives phase events after they are written to the local logger.
// A nil sink leaves logging unchanged. A sink error does not change span status.
type PhaseSink interface {
	PersistPhase(context.Context, PhaseEvent) error
}

// WithPhaseSink attaches the sink StartRun copies onto the execution.
// Installing it after StartRun does not affect that execution.
func WithPhaseSink(ctx context.Context, sink PhaseSink) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, phaseSinkKey{}, sink)
}

type trace struct {
	mu              sync.Mutex
	log             *zap.Logger
	sink            PhaseSink
	persistCtx      context.Context
	runID           string
	executionID     string
	sequence        uint64
	spans           uint64
	models          int
	tools           int
	compressions    int
	persistFailures int
}

type phaseOutcome struct {
	status   string
	duration float64
	errText  string
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
	executionID := uuid.NewString()
	log := Logger(ctx).With(zap.String("session_id", identity.SessionID), zap.String("run_id", identity.RunID),
		zap.String("execution_id", executionID), zap.String("worker_id", identity.WorkerID))
	persistCtx := context.Background()
	var sink PhaseSink
	if ctx != nil {
		persistCtx = context.WithoutCancel(ctx)
		sink, _ = ctx.Value(phaseSinkKey{}).(PhaseSink)
	}
	ctx = context.WithValue(ctx, traceKey{}, &trace{
		log: log, sink: sink, persistCtx: persistCtx, runID: identity.RunID, executionID: executionID,
	})
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
	s.emitLocked("started", nil, nil)
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
		duration := float64(time.Since(s.started)) / float64(time.Millisecond)
		fields = append(fields, zap.String("status", status), zap.Float64("duration_ms", duration))
		errText := ""
		if err != nil {
			fields = append(fields, zap.Error(err))
			errText = err.Error()
		}
		s.trace.mu.Lock()
		defer s.trace.mu.Unlock()
		if s.phase == "run" {
			fields = append(fields, zap.Int("model_calls", s.trace.models), zap.Int("tool_calls", s.trace.tools), zap.Int("compressions", s.trace.compressions))
		}
		s.emitLocked("finished", fields, &phaseOutcome{status: status, duration: duration, errText: errText})
	})
}

func (s *Span) emitLocked(event string, fields []zap.Field, outcome *phaseOutcome) {
	s.trace.sequence++
	base := []zap.Field{zap.Uint64("sequence", s.trace.sequence), zap.Uint64("span_id", s.id),
		zap.Uint64("parent_span_id", s.parent), zap.String("phase", s.phase), zap.String("name", s.name), zap.String("event", event)}
	base = append(base, s.fields...)
	base = append(base, fields...)
	if s.trace.sink != nil && s.phase == "run" && event == "finished" {
		base = append(base, zap.Int("phase_persist_failures", s.trace.persistFailures))
	}
	s.trace.log.Info("agent.phase", base...)
	if s.trace.sink == nil {
		return
	}
	record := PhaseEvent{
		RunID: s.trace.runID, ExecutionID: s.trace.executionID, Sequence: s.trace.sequence,
		SpanID: s.id, ParentSpanID: s.parent, Phase: s.phase, Name: s.name, Event: event,
	}
	if outcome != nil {
		record.Status = outcome.status
		record.DurationMS = outcome.duration
		record.HasDuration = true
		record.Error = outcome.errText
	}
	if err := s.trace.sink.PersistPhase(s.trace.persistCtx, record); err != nil {
		s.trace.persistFailures++
	}
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
