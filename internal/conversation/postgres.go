package conversation

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var migration string

type Postgres struct{ pool *pgxpool.Pool }

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
	return &Postgres{pool: pool}, nil
}
func (p *Postgres) Close() { p.pool.Close() }
func (p *Postgres) Create(ctx context.Context, user string) (Session, error) {
	if err := ValidateUser(user); err != nil {
		return Session{}, err
	}
	s := Session{ID: uuid.NewString(), Username: user}
	err := p.pool.QueryRow(ctx, "INSERT INTO agent_sessions(id,username) VALUES($1,$2) RETURNING created_at,updated_at", s.ID, user).Scan(&s.CreatedAt, &s.UpdatedAt)
	return s, err
}
func (p *Postgres) List(ctx context.Context, user string, limit, offset int) ([]Session, error) {
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
	return lease, nil
}

type pgLease struct {
	conn      *pgxpool.Conn
	id        string
	messages  []*schema.AgenticMessage
	committed bool
}

func (l *pgLease) Messages() []*schema.AgenticMessage { return l.messages }
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
