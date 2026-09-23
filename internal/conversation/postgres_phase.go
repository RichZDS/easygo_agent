package conversation

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

func (p *Postgres) AppendRunPhase(ctx context.Context, phase RunPhase) error {
	if _, err := uuid.Parse(phase.RunID); err != nil {
		return errors.New("run phase run_id must be a UUID")
	}
	if _, err := uuid.Parse(phase.ExecutionID); err != nil {
		return errors.New("run phase execution_id must be a UUID")
	}
	var status any
	if phase.Status != "" {
		status = phase.Status
	}
	var duration any
	if phase.DurationMS != nil {
		duration = *phase.DurationMS
	}
	var phaseErr any
	if phase.Error != "" {
		phaseErr = phase.Error
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO agent_run_phases
		(run_id, execution_id, sequence, span_id, parent_span_id, phase, name, event, status, duration_ms, error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		phase.RunID, phase.ExecutionID, phase.Sequence, phase.SpanID, phase.ParentSpanID,
		phase.Phase, phase.Name, phase.Event, status, duration, phaseErr)
	return err
}

func (p *Postgres) ListRunPhases(ctx context.Context, user, sessionID, runID string, afterSequence int64) (RunPhasePage, error) {
	if err := ValidateUser(user); err != nil {
		return RunPhasePage{}, err
	}
	if _, err := uuid.Parse(sessionID); err != nil {
		return RunPhasePage{}, ErrNotFound
	}
	if _, err := uuid.Parse(runID); err != nil {
		return RunPhasePage{}, ErrNotFound
	}
	var exists bool
	err := p.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM agent_runs r
		JOIN agent_sessions s ON s.id = r.session_id
		WHERE s.id = $1 AND s.username = $2 AND r.id = $3)`, sessionID, user, runID).Scan(&exists)
	if err != nil {
		return RunPhasePage{}, err
	}
	if !exists {
		return RunPhasePage{}, ErrNotFound
	}
	rows, err := p.pool.Query(ctx, `SELECT execution_id::text, sequence, span_id, parent_span_id, phase, name, event,
		COALESCE(status, ''), duration_ms, COALESCE(error, ''), created_at
		FROM agent_run_phases
		WHERE run_id = $1 AND sequence > $2
		ORDER BY sequence, execution_id
		LIMIT $3`, runID, afterSequence, phasePageLimit)
	if err != nil {
		return RunPhasePage{}, err
	}
	defer rows.Close()
	phases := make([]RunPhase, 0)
	for rows.Next() {
		var phase RunPhase
		if err := rows.Scan(&phase.ExecutionID, &phase.Sequence, &phase.SpanID, &phase.ParentSpanID, &phase.Phase, &phase.Name, &phase.Event, &phase.Status, &phase.DurationMS, &phase.Error, &phase.CreatedAt); err != nil {
			return RunPhasePage{}, err
		}
		phase.RunID = runID
		phases = append(phases, phase)
	}
	if err := rows.Err(); err != nil {
		return RunPhasePage{}, err
	}
	return pagePhases(phases, afterSequence), nil
}

var _ PhaseStore = (*Postgres)(nil)
