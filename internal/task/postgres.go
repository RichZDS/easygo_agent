package task

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var migration string

// Postgres shares the conversation database, but never its per-session lease.
type Postgres struct{ pool *pgxpool.Pool }

func NewPostgres(ctx context.Context, pool *pgxpool.Pool) (*Postgres, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(724069511)"); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, migration); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Postgres{pool}, nil
}
func read(row pgx.Row) (Task, error) {
	var t Task
	var data []byte
	err := row.Scan(&data, &t.Token, &t.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	err = json.Unmarshal(data, &t)
	return t, err
}

const columns = "state,COALESCE(claim_token::text,''),COALESCE(lease_expires_at,'epoch'::timestamptz)"

func (p *Postgres) Create(ctx context.Context, o Owner, b Brief) (Task, error) {
	t, err := newTask(o, b)
	if err != nil {
		return Task{}, err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return Task{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback(context.Background())
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r JOIN agent_sessions s ON s.id=r.session_id WHERE r.id=$1 AND s.id=$2 AND s.username=$3 AND r.status='running' AND NOT r.cancel_requested)`, o.Run, o.Session, o.User).Scan(&valid)
	if err != nil {
		return Task{}, err
	}
	if !valid {
		return Task{}, ErrNotFound
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent_tasks(id,username,session_id,parent_run_id,request_key,status,version,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(parent_run_id,request_key) DO NOTHING`, t.ID, o.User, o.Session, o.Run, b.Key, t.Status, t.Version, data)
	if err != nil {
		return Task{}, err
	}
	if tag.RowsAffected() == 0 {
		old, err := read(tx.QueryRow(ctx, "SELECT "+columns+" FROM agent_tasks WHERE parent_run_id=$1 AND request_key=$2", o.Run, b.Key))
		if err != nil {
			return Task{}, err
		}
		if old.RequestFingerprint != briefFingerprint(b) {
			return Task{}, ErrConflict
		}
		return old, tx.Commit(ctx)
	}
	if err = p.persist(ctx, tx, t); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return t, nil
}
func (p *Postgres) Get(ctx context.Context, o Owner, id string) (Task, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Task{}, ErrNotFound
	}
	return read(p.pool.QueryRow(ctx, "SELECT "+columns+" FROM agent_tasks WHERE id=$1 AND username=$2 AND session_id=$3", id, o.User, o.Session))
}
func (p *Postgres) List(ctx context.Context, o Owner) ([]Task, error) {
	rows, err := p.pool.Query(ctx, "SELECT "+columns+" FROM agent_tasks WHERE username=$1 AND session_id=$2 ORDER BY created_at,id", o.User, o.Session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := read(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (p *Postgres) persist(ctx context.Context, tx pgx.Tx, t Task) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE agent_tasks SET state=$2,status=$3,version=$4,claim_token=NULLIF($5,'')::uuid,lease_expires_at=$6 WHERE id=$1", t.ID, data, t.Status, t.Version, t.Token, t.LeaseUntil)
	if err != nil {
		return err
	}
	for _, e := range t.Events {
		data, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO agent_task_events(task_id,seq,version,event) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", t.ID, e.Seq, e.Version, data); err != nil {
			return err
		}
	}
	for _, c := range t.Calls {
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO agent_task_calls(task_id,call_key,record) VALUES($1,$2,$3) ON CONFLICT(task_id,call_key) DO UPDATE SET record=EXCLUDED.record", t.ID, c.Key, data); err != nil {
			return err
		}
	}
	if terminal(t.Status) {
		_, err = tx.Exec(ctx, "INSERT INTO agent_task_notifications(id,username,session_id,content) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", t.NotificationID(), t.Owner.User, t.Owner.Session, t.Notification())
	}
	return err
}
func (p *Postgres) Claim(ctx context.Context, ttl time.Duration) (Task, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback(context.Background())
	t, err := read(tx.QueryRow(ctx, "SELECT "+columns+" FROM agent_tasks WHERE status='queued' OR (status='running' AND lease_expires_at<=now()) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1"))
	if errors.Is(err, ErrNotFound) {
		return Task{}, ErrNoTask
	}
	if err != nil {
		return Task{}, err
	}
	t.Status = Running
	t.Token = uuid.NewString()
	t.LeaseUntil = time.Now().UTC().Add(ttl)
	t.Event("running", "worker claimed execution")
	if err = p.persist(ctx, tx, t); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return t, nil
}
func (p *Postgres) Save(ctx context.Context, t Task, token string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	old, err := read(tx.QueryRow(ctx, "SELECT "+columns+" FROM agent_tasks WHERE id=$1 AND status='running' AND claim_token=$2 AND lease_expires_at>now() FOR UPDATE", t.ID, token))
	if errors.Is(err, ErrNotFound) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	t.Token = token
	t.LeaseUntil = old.LeaseUntil
	if err = p.persist(ctx, tx, t); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (p *Postgres) Heartbeat(ctx context.Context, id, token string, ttl time.Duration) error {
	tag, err := p.pool.Exec(ctx, "UPDATE agent_tasks SET lease_expires_at=$3 WHERE id=$1 AND status='running' AND claim_token=$2 AND lease_expires_at>now()", id, token, time.Now().UTC().Add(ttl))
	if err == nil && tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return err
}
func (p *Postgres) Release(ctx context.Context, id, token string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	t, err := read(tx.QueryRow(ctx, "SELECT "+columns+" FROM agent_tasks WHERE id=$1 AND status='running' AND claim_token=$2 FOR UPDATE", id, token))
	if errors.Is(err, ErrNotFound) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	t.Status = Queued
	t.Token = ""
	t.LeaseUntil = time.Time{}
	if err = p.persist(ctx, tx, t); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (p *Postgres) change(ctx context.Context, o Owner, id string, fn func(*Task) error) (Task, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Task{}, ErrNotFound
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback(context.Background())
	t, err := read(tx.QueryRow(ctx, "SELECT "+columns+" FROM agent_tasks WHERE id=$1 AND username=$2 AND session_id=$3 FOR UPDATE", id, o.User, o.Session))
	if err != nil {
		return Task{}, err
	}
	if err = fn(&t); err != nil {
		return Task{}, err
	}
	if err = p.persist(ctx, tx, t); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return t, nil
}
func (p *Postgres) Cancel(ctx context.Context, o Owner, id string) (Task, error) {
	return p.change(ctx, o, id, cancel)
}
func (p *Postgres) Resume(ctx context.Context, o Owner, id string, in Resume) (Task, error) {
	return p.change(ctx, o, id, func(t *Task) error { return resume(t, in) })
}
func (p *Postgres) UpdatePlan(ctx context.Context, o Owner, id string, steps []PlanStep) (Task, error) {
	return p.change(ctx, o, id, func(t *Task) error {
		if t.Status == Running {
			return ErrBusy
		}
		t.Plan = steps
		t.Event("plan", "plan updated")
		return nil
	})
}
func (p *Postgres) Pending(ctx context.Context) ([]Notification, error) {
	rows, err := p.pool.Query(ctx, "SELECT id,username,session_id::text,content FROM agent_task_notifications WHERE NOT enqueued ORDER BY created_at,id LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err = rows.Scan(&n.ID, &n.Owner.User, &n.Owner.Session, &n.Content); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (p *Postgres) MarkEnqueued(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, "UPDATE agent_task_notifications SET enqueued=true WHERE id=$1", id)
	return err
}
