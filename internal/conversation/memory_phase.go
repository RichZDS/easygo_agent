package conversation

import (
	"context"
	"errors"
	"time"
)

func (m *Memory) AppendRunPhase(ctx context.Context, phase RunPhase) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if phase.RunID == "" || phase.ExecutionID == "" {
		return errors.New("run phase is missing identity")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.findRunLocked(phase.RunID)
	if run == nil {
		return ErrNotFound
	}
	for _, existing := range run.phases {
		if existing.ExecutionID == phase.ExecutionID && existing.Sequence == phase.Sequence {
			return errors.New("run phase already exists")
		}
	}
	if phase.CreatedAt.IsZero() {
		phase.CreatedAt = time.Now().UTC()
	}
	run.phases = append(run.phases, cloneRunPhase(phase))
	return nil
}

func (m *Memory) ListRunPhases(ctx context.Context, user, sessionID, runID string, afterSequence int64) (RunPhasePage, error) {
	if err := ctx.Err(); err != nil {
		return RunPhasePage{}, err
	}
	if err := ValidateUser(user); err != nil {
		return RunPhasePage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[sessionID]
	if entry == nil || entry.session.Username != user {
		return RunPhasePage{}, ErrNotFound
	}
	var run *memoryRun
	for _, candidate := range entry.runs {
		if candidate.record.ID == runID {
			run = candidate
			break
		}
	}
	if run == nil {
		return RunPhasePage{}, ErrNotFound
	}
	return pagePhases(run.phases, afterSequence), nil
}

func (m *Memory) findRunLocked(runID string) *memoryRun {
	for _, entry := range m.entries {
		for _, run := range entry.runs {
			if run.record.ID == runID {
				return run
			}
		}
	}
	return nil
}

var _ PhaseStore = (*Memory)(nil)
