package agentruntime

import (
	"context"

	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/conversation"
)

type runPhaseStoreKey struct{}

func withRunPhaseStore(ctx context.Context, store any) context.Context {
	if ctx == nil || store == nil {
		return ctx
	}
	phaseStore, ok := store.(conversation.PhaseStore)
	if !ok || phaseStore == nil {
		return ctx
	}
	return context.WithValue(ctx, runPhaseStoreKey{}, phaseStore)
}

func attachPhaseSink(ctx context.Context) context.Context {
	phaseStore, _ := ctx.Value(runPhaseStoreKey{}).(conversation.PhaseStore)
	if phaseStore == nil {
		return ctx
	}
	return telemetry.WithPhaseSink(ctx, phaseStoreSink{store: phaseStore})
}

type phaseStoreSink struct {
	store conversation.PhaseStore
}

func (s phaseStoreSink) PersistPhase(ctx context.Context, event telemetry.PhaseEvent) error {
	record := conversation.RunPhase{
		RunID:        event.RunID,
		ExecutionID:  event.ExecutionID,
		Sequence:     int64(event.Sequence),
		SpanID:       int64(event.SpanID),
		ParentSpanID: int64(event.ParentSpanID),
		Phase:        event.Phase,
		Name:         event.Name,
		Event:        event.Event,
		Status:       event.Status,
		Error:        event.Error,
	}
	if event.HasDuration {
		duration := event.DurationMS
		record.DurationMS = &duration
	}
	return s.store.AppendRunPhase(ctx, record)
}

var _ telemetry.PhaseSink = phaseStoreSink{}
