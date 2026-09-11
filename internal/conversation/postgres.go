package conversation

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var migration string

type Postgres struct {
	pool         *pgxpool.Pool
	maxPendingMu sync.RWMutex
	maxPending   int
}

func NewPostgres(ctx context.Context, dsn string, maxConns int32) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid database DSN")
	}
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	// Serialize bootstrap across gateway replicas. No migration runs during inference.
	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(724069510)")
	if err == nil {
		_, err = tx.Exec(ctx, migration)
	}
	if err == nil {
		err = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Postgres{pool: pool, maxPending: DefaultMaxPendingRuns}, nil
}
func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) SetMaxPendingRuns(maxPending int) {
	if maxPending < 1 {
		maxPending = DefaultMaxPendingRuns
	}
	p.maxPendingMu.Lock()
	p.maxPending = maxPending
	p.maxPendingMu.Unlock()
}

func (p *Postgres) pendingLimit() int {
	p.maxPendingMu.RLock()
	maxPending := p.maxPending
	p.maxPendingMu.RUnlock()
	if maxPending < 1 {
		return DefaultMaxPendingRuns
	}
	return maxPending
}
func (p *Postgres) Create(ctx context.Context, user string) (Session, error) {
	if err := ValidateUser(user); err != nil {
		return Session{}, err
	}
	s := Session{ID: uuid.NewString(), Username: user}
	err := p.pool.QueryRow(ctx, "INSERT INTO agent_sessions(id,username) VALUES($1,$2) RETURNING created_at,updated_at", s.ID, user).Scan(&s.CreatedAt, &s.UpdatedAt)
	return s, err
}
func (p *Postgres) List(ctx context.Context, user string, limit, offset int) ([]Session, error) {
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, "SELECT id::text,username,created_at,updated_at FROM agent_sessions WHERE username=$1 ORDER BY updated_at DESC,id LIMIT $2 OFFSET $3", user, pageSize(limit), max(offset, 0))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Session{}
	for rows.Next() {
		var s Session
		if err = rows.Scan(&s.ID, &s.Username, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}
func (p *Postgres) History(ctx context.Context, user, id string, after int64, limit int) ([]Turn, error) {
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	var exists bool
	err := p.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent_sessions WHERE id=$1 AND username=$2)", id, user).Scan(&exists)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := p.pool.Query(ctx, "SELECT id,status,messages,created_at FROM agent_turns WHERE session_id=$1 AND id>$2 ORDER BY id LIMIT $3", id, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Turn{}
	for rows.Next() {
		var t Turn
		var data []byte
		if err = rows.Scan(&t.ID, &t.Status, &data, &t.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &t.Messages); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}
func (p *Postgres) Begin(ctx context.Context, user, id string) (Lease, error) {
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, ErrNotFound
	}
	id = parsed.String()
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", id).Scan(&locked)
	if err != nil || !locked {
		conn.Release()
		if err != nil {
			return nil, err
		}
		return nil, ErrBusy
	}
	lease := &pgLease{conn: conn, id: id}
	var data []byte
	err = conn.QueryRow(ctx, "SELECT context_messages FROM agent_sessions WHERE id=$1 AND username=$2", id, user).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(data, &lease.messages)
	}
	if err != nil {
		lease.Close()
		return nil, err
	}
	var pending bool
	err = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent_runs WHERE session_id=$1 AND status IN ('queued','running'))", id).Scan(&pending)
	if err != nil {
		lease.Close()
		return nil, err
	}
	if pending {
		lease.Close()
		return nil, ErrBusy
	}
	return lease, nil
}

type pgLease struct {
	conn      *pgxpool.Conn
	id        string
	messages  []*schema.AgenticMessage
	committed bool
}

func (l *pgLease) Messages() []*schema.AgenticMessage {
	result, err := Clone(l.messages)
	if err != nil {
		return nil
	}
	return result
}
func (l *pgLease) Commit(ctx context.Context, messages []*schema.AgenticMessage, turn Turn) error {
	if l.conn == nil || l.committed {
		return ErrBusy
	}
	state, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	audit, err := json.Marshal(turn.Messages)
	if err != nil {
		return err
	}
	tx, err := l.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	_, err = tx.Exec(ctx, "INSERT INTO agent_turns(session_id,status,messages) VALUES($1,$2,$3)", l.id, turn.Status, audit)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE agent_sessions SET context_messages=$2,updated_at=now() WHERE id=$1", l.id, state)
	if err != nil {
		return err
	}
	err = tx.Commit(ctx)
	if err == nil {
		l.committed = true
	}
	return err
}
func (l *pgLease) Close() {
	if l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := l.conn.Exec(ctx, "SELECT pg_advisory_unlock(hashtextextended($1,0))", l.id)
	if err != nil {
		_ = l.conn.Conn().Close(ctx)
	} // Never return a possibly locked connection to the pool.
	l.conn.Release()
	l.conn = nil
}

// Enqueue inserts a durable request while serializing against other requests
// for the same session. The session lock makes the pending-run limit and the
// returned position consistent with concurrent submissions.
func (p *Postgres) Enqueue(ctx context.Context, user, sessionID, input, idempotencyKey string) (RunRecord, error) {
	if err := ValidateUser(user); err != nil {
		return RunRecord{}, err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return RunRecord{}, ErrEmptyInput
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if _, err := uuid.Parse(sessionID); err != nil {
		return RunRecord{}, ErrNotFound
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return RunRecord{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent_sessions WHERE id=$1 AND username=$2)", sessionID, user).Scan(&exists); err != nil {
		return RunRecord{}, err
	}
	if !exists {
		return RunRecord{}, ErrNotFound
	}
	var sessionLock string
	if err = tx.QueryRow(ctx, "SELECT id::text FROM agent_sessions WHERE id=$1 FOR UPDATE", sessionID).Scan(&sessionLock); err != nil {
		return RunRecord{}, err
	}
	if idempotencyKey != "" {
		var r RunRecord
		err = tx.QueryRow(ctx, runSelect+" WHERE r.session_id=$1 AND r.idempotency_key=$2", sessionID, idempotencyKey).Scan(runArgs(&r)...)
		if err == nil {
			r = normalizeRun(r)
			if r.Input != input {
				return RunRecord{}, ErrIdempotencyConflict
			}
			if err = tx.Commit(ctx); err != nil {
				return RunRecord{}, err
			}
			return p.position(ctx, r)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return RunRecord{}, err
		}
	}
	var n int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM agent_runs WHERE session_id=$1 AND status='queued'", sessionID).Scan(&n); err != nil {
		return RunRecord{}, err
	}
	maxPending := p.pendingLimit()
	if n >= maxPending {
		return RunRecord{}, ErrQueueFull
	}
	id := uuid.NewString()
	var createdAt time.Time
	if err = tx.QueryRow(ctx, "INSERT INTO agent_runs(id,session_id,input,status,idempotency_key) VALUES($1,$2,$3,'queued',$4) RETURNING created_at", id, sessionID, input, idempotencyKey).Scan(&createdAt); err != nil {
		return RunRecord{}, err
	}
	r := RunRecord{ID: id, SessionID: sessionID, Input: input, Status: RunQueued, IdempotencyKey: idempotencyKey, CreatedAt: createdAt, Position: n + 1}
	_, err = tx.Exec(ctx, "UPDATE agent_sessions SET updated_at=now() WHERE id=$1", sessionID)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return RunRecord{}, err
	}
	return p.position(ctx, r)
}

const runSelect = `SELECT r.id::text,r.session_id::text,r.input,r.status,COALESCE(r.idempotency_key,''),r.cancel_requested,COALESCE(r.result_text,''),COALESCE(r.error,''),COALESCE(r.turn_id,0),r.created_at,r.started_at,r.finished_at,COALESCE(r.worker_id,''),COALESCE(r.lease_expires_at,'epoch'::timestamptz),COALESCE(r.claim_token::text,'') FROM agent_runs r`

func runArgs(r *RunRecord) []any {
	return []any{&r.ID, &r.SessionID, &r.Input, &r.Status, &r.IdempotencyKey, &r.CancelRequested, &r.ResultText, &r.Error, &r.TurnID, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.WorkerID, &r.LeaseExpiresAt, &r.ClaimToken}
}

func normalizeRun(r RunRecord) RunRecord {
	if r.LeaseExpiresAt.Equal(time.Unix(0, 0).UTC()) {
		r.LeaseExpiresAt = time.Time{}
	}
	return r
}

func (p *Postgres) position(ctx context.Context, r RunRecord) (RunRecord, error) {
	if r.Status == RunQueued {
		if err := p.pool.QueryRow(ctx, "SELECT count(*)+1 FROM agent_runs WHERE session_id=$1 AND status='queued' AND (created_at<$2 OR (created_at=$2 AND id<$3))", r.SessionID, r.CreatedAt, r.ID).Scan(&r.Position); err != nil {
			return RunRecord{}, err
		}
	}
	return r, nil
}

func (p *Postgres) GetRun(ctx context.Context, user, sessionID, runID string) (RunRecord, error) {
	if err := ValidateUser(user); err != nil {
		return RunRecord{}, err
	}
	if _, err := uuid.Parse(sessionID); err != nil {
		return RunRecord{}, ErrNotFound
	}
	if _, err := uuid.Parse(runID); err != nil {
		return RunRecord{}, ErrNotFound
	}
	var r RunRecord
	err := p.pool.QueryRow(ctx, runSelect+" JOIN agent_sessions s ON s.id=r.session_id WHERE s.id=$1 AND s.username=$2 AND r.id=$3", sessionID, user, runID).Scan(runArgs(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunRecord{}, ErrNotFound
	}
	if err != nil {
		return RunRecord{}, err
	}
	r = normalizeRun(r)
	return p.position(ctx, r)
}

func (p *Postgres) ListRuns(ctx context.Context, user, sessionID string, limit int) ([]RunRecord, error) {
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(sessionID); err != nil {
		return nil, ErrNotFound
	}
	var ok bool
	if err := p.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent_sessions WHERE id=$1 AND username=$2)", sessionID, user).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	rows, err := p.pool.Query(ctx, runSelect+" WHERE r.session_id=$1 AND r.status IN ('queued','running') ORDER BY r.created_at,r.id LIMIT $2", sessionID, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RunRecord{}
	for rows.Next() {
		var r RunRecord
		if err := rows.Scan(runArgs(&r)...); err != nil {
			return nil, err
		}
		r = normalizeRun(r)
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close the result set before position lookups so a small connection pool
	// cannot deadlock while calculating each queued item's dynamic position.
	rows.Close()
	for i := range result {
		result[i], err = p.position(ctx, result[i])
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (p *Postgres) RequestCancel(ctx context.Context, user, sessionID, runID string) (RunRecord, error) {
	if err := ValidateUser(user); err != nil {
		return RunRecord{}, err
	}
	if _, err := uuid.Parse(sessionID); err != nil {
		return RunRecord{}, ErrNotFound
	}
	if _, err := uuid.Parse(runID); err != nil {
		return RunRecord{}, ErrNotFound
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return RunRecord{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var r RunRecord
	// Lock only the run row. Session ownership is checked by the join, and
	// avoiding a session lock here keeps cancellation in the same run-then-
	// session order as CommitRun.
	err = tx.QueryRow(ctx, runSelect+" JOIN agent_sessions s ON s.id=r.session_id WHERE r.session_id=$1 AND r.id=$2 AND s.username=$3 FOR UPDATE OF r", sessionID, runID, user).Scan(runArgs(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunRecord{}, ErrNotFound
	}
	if err != nil {
		return RunRecord{}, err
	}
	r = normalizeRun(r)
	switch r.Status {
	case RunQueued:
		_, err = tx.Exec(ctx, "UPDATE agent_runs SET status='canceled',finished_at=now() WHERE id=$1", runID)
	case RunRunning:
		_, err = tx.Exec(ctx, "UPDATE agent_runs SET cancel_requested=true WHERE id=$1", runID)
	}
	if err == nil {
		if r.Status == RunQueued || r.Status == RunRunning {
			_, err = tx.Exec(ctx, "UPDATE agent_sessions SET updated_at=now() WHERE id=$1", sessionID)
		}
	}
	if err == nil {
		err = tx.QueryRow(ctx, runSelect+" WHERE r.id=$1", runID).Scan(runArgs(&r)...)
		if err == nil {
			r = normalizeRun(r)
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return RunRecord{}, err
	}
	return p.position(ctx, r)
}

func (p *Postgres) ClaimNext(ctx context.Context, workerID string, leaseTTL time.Duration) (RunLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workerID) == "" {
		return nil, errors.New("worker ID cannot be empty")
	}
	if leaseTTL <= 0 {
		leaseTTL = 30 * time.Second
	}
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		return nil, err
	}
	rollback := true
	released := false
	release := func() {
		if rollback {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			rollbackErr := tx.Rollback(cleanup)
			cancel()
			rollback = false
			if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				// Do not put a connection with an unknown transaction state back
				// into the pool.
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = conn.Conn().Close(cleanup)
				cancel()
				conn.Release()
				released = true
				return
			}
		}
		if !released {
			conn.Release()
			released = true
		}
	}
	defer func() {
		if rollback {
			_ = tx.Rollback(context.Background())
		}
		if !released {
			conn.Release()
		}
	}()

	// A bounded candidate batch lets a busy session be skipped while another
	// session can still make progress. The transaction-scoped advisory lock
	// serializes claim checks without preventing lease-expiry recovery when an
	// old worker keeps its database connection alive.
	rows, err := tx.Query(ctx, runSelect+" WHERE r.status='queued' ORDER BY r.created_at,r.id LIMIT 64 FOR UPDATE SKIP LOCKED")
	if err != nil {
		release()
		return nil, err
	}
	candidates := make([]RunRecord, 0, 64)
	for rows.Next() {
		var r RunRecord
		if err = rows.Scan(runArgs(&r)...); err != nil {
			rows.Close()
			release()
			return nil, err
		}
		r = normalizeRun(r)
		candidates = append(candidates, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		release()
		return nil, err
	}
	for _, r := range candidates {
		var locked bool
		if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))", r.SessionID).Scan(&locked); err != nil {
			release()
			return nil, err
		}
		if !locked {
			continue
		}
		unlock := func() {}

		var alreadyRunning bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent_runs WHERE session_id=$1 AND status='running')", r.SessionID).Scan(&alreadyRunning); err != nil {
			unlock()
			release()
			return nil, err
		}
		var earlierQueued bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM agent_runs x
			WHERE x.session_id=$1 AND x.status='queued'
			  AND (x.created_at<$2 OR (x.created_at=$2 AND x.id<$3))
			)`, r.SessionID, r.CreatedAt, r.ID).Scan(&earlierQueued); err != nil {
			unlock()
			release()
			return nil, err
		}
		if alreadyRunning || earlierQueued {
			unlock()
			continue
		}

		var data []byte
		var username string
		if err = tx.QueryRow(ctx, "SELECT username, context_messages FROM agent_sessions WHERE id=$1 FOR UPDATE", r.SessionID).Scan(&username, &data); err != nil {
			unlock()
			release()
			return nil, err
		}
		var messages []*schema.AgenticMessage
		if err = json.Unmarshal(data, &messages); err != nil {
			unlock()
			release()
			return nil, err
		}
		token := uuid.NewString()
		now := time.Now().UTC()
		expiry := now.Add(leaseTTL)
		cmd, updateErr := tx.Exec(ctx, `UPDATE agent_runs
			SET status='running',worker_id=$2,claim_token=$3,lease_expires_at=$4,started_at=$5
			WHERE id=$1 AND status='queued'`, r.ID, workerID, token, expiry, now)
		if updateErr != nil {
			unlock()
			release()
			return nil, updateErr
		}
		if cmd.RowsAffected() != 1 {
			unlock()
			continue
		}
		if err = tx.Commit(ctx); err != nil {
			unlock()
			release()
			return nil, err
		}
		rollback = false
		released = true // ownership moves to pgRunLease
		r.Status = RunRunning
		r.Username = username
		r.WorkerID = workerID
		r.ClaimToken = token
		r.LeaseExpiresAt = expiry
		r.StartedAt = &now
		return &pgRunLease{conn: conn, run: r, claimToken: token, sessionID: r.SessionID, messages: messages}, nil
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = tx.Rollback(cleanup)
	cancel()
	rollback = false
	release()
	return nil, ErrNoQueuedRun
}

func (p *Postgres) RecoverExpired(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `UPDATE agent_runs SET status=CASE WHEN cancel_requested THEN 'canceled' ELSE 'queued' END,
		finished_at=CASE WHEN cancel_requested THEN $1 ELSE NULL END, started_at=CASE WHEN cancel_requested THEN started_at ELSE NULL END,
		worker_id=NULL, claim_token=NULL, lease_expires_at=NULL
		WHERE status='running' AND lease_expires_at IS NOT NULL AND lease_expires_at <= $1`, now)
	return err
}

type pgRunLease struct {
	conn       *pgxpool.Conn
	run        RunRecord
	claimToken string
	sessionID  string
	messages   []*schema.AgenticMessage
	mu         sync.Mutex
	closed     bool
	committed  bool
}

func (l *pgRunLease) Run() RunRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return CloneRunRecord(l.run)
}

func (l *pgRunLease) Messages() []*schema.AgenticMessage {
	result, err := Clone(l.messages)
	if err != nil {
		return nil
	}
	return result
}

func (l *pgRunLease) CommitRun(ctx context.Context, next []*schema.AgenticMessage, status RunStatus, outputs []*schema.AgenticMessage, resultText, runError string) error {
	if status != RunCompleted && status != RunFailed && status != RunCanceled {
		return ErrInvalidRunStatus
	}
	state, err := json.Marshal(next)
	if err != nil {
		return err
	}
	auditMessages, err := runAudit(l.run.Input, outputs)
	if err != nil {
		return err
	}
	audit, err := json.Marshal(auditMessages)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.committed {
		return ErrBusy
	}
	if l.conn == nil {
		return ErrLeaseLost
	}
	tx, err := l.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var cancelRequested bool
	if err = tx.QueryRow(ctx, "SELECT cancel_requested FROM agent_runs WHERE id=$1 AND status='running' AND claim_token=$2 FOR UPDATE", l.run.ID, l.claimToken).Scan(&cancelRequested); errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	} else if err != nil {
		return err
	}
	if status == RunCompleted && cancelRequested {
		return ErrCancellationRequested
	}
	var turnID int64
	if err = tx.QueryRow(ctx, "INSERT INTO agent_turns(session_id,status,messages) VALUES($1,$2,$3) RETURNING id", l.run.SessionID, status, audit).Scan(&turnID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE agent_sessions SET context_messages=$2,updated_at=now() WHERE id=$1", l.run.SessionID, state); err != nil {
		return err
	}
	now := time.Now().UTC()
	cmd, err := tx.Exec(ctx, `UPDATE agent_runs SET status=$2,cancel_requested=CASE WHEN $2='canceled' THEN cancel_requested ELSE false END,result_text=$3,error=$4,turn_id=$5,finished_at=$6,worker_id=NULL,claim_token=NULL,lease_expires_at=NULL WHERE id=$1 AND status='running' AND claim_token=$7`, l.run.ID, status, resultText, runError, turnID, now, l.claimToken)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	l.committed = true
	l.run.Status = status
	if status != RunCanceled {
		l.run.CancelRequested = false
	}
	l.run.ResultText = resultText
	l.run.Error = runError
	l.run.TurnID = turnID
	l.run.FinishedAt = &now
	l.run.WorkerID = ""
	l.run.ClaimToken = ""
	l.run.LeaseExpiresAt = time.Time{}
	return nil
}
func (l *pgRunLease) CancelRequested(ctx context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.conn == nil {
		return false, ErrBusy
	}
	var requested bool
	err := l.conn.QueryRow(ctx, "SELECT cancel_requested FROM agent_runs WHERE id=$1 AND status='running' AND claim_token=$2", l.run.ID, l.claimToken).Scan(&requested)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrLeaseLost
	}
	return requested, err
}
func (l *pgRunLease) Heartbeat(ctx context.Context, ttl time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.conn == nil {
		return ErrBusy
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	expiry := time.Now().UTC().Add(ttl)
	cmd, err := l.conn.Exec(ctx, "UPDATE agent_runs SET lease_expires_at=$3 WHERE id=$1 AND status='running' AND claim_token=$2", l.run.ID, l.claimToken, expiry)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	l.run.LeaseExpiresAt = expiry
	return nil
}
func (l *pgRunLease) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	committed := l.committed
	conn := l.conn
	l.closed = true
	l.conn = nil
	l.mu.Unlock()
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !committed {
		_, _ = conn.Exec(ctx, `UPDATE agent_runs
			SET status=CASE WHEN cancel_requested THEN 'canceled' ELSE 'queued' END,
				finished_at=CASE WHEN cancel_requested THEN now() ELSE NULL END,
				started_at=CASE WHEN cancel_requested THEN started_at ELSE NULL END,
				worker_id=NULL,claim_token=NULL,lease_expires_at=NULL
			WHERE id=$1 AND status='running' AND claim_token=$2`, l.run.ID, l.claimToken)
	}
	conn.Release()
}

var _ QueueStore = (*Postgres)(nil)
var _ RunLease = (*pgRunLease)(nil)
