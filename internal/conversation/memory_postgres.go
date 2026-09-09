package conversation

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed memory_schema.sql
var memoryMigration string

// MemoryPostgres owns a dedicated long-term-memory database. It deliberately
// does not implement Store: it cannot create/read conversation sessions.
type MemoryPostgres struct{ pool *pgxpool.Pool }

func NewMemoryPostgres(ctx context.Context, dsn string, maxConns int32) (*MemoryPostgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid memory database DSN")
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(724069511)")
	if err == nil {
		_, err = tx.Exec(ctx, memoryMigration)
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
	return &MemoryPostgres{pool: pool}, nil
}

func (p *MemoryPostgres) Close() { p.pool.Close() }

var _ MemoryStore = (*MemoryPostgres)(nil)
