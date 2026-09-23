package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const longTermMemoryColumns = `id::text,username,kind,content,tags,importance,confidence,source_sessions,source_turn_ids,call_count,first_seen_at,last_seen_at,last_accessed_at,expires_at,created_at,updated_at,archived_at,COALESCE(profile_slot,0),version,state,superseded_by::text`

func (p *MemoryPostgres) Recall(ctx context.Context, user string, limit int) ([]LongTermMemory, error) {
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxProfileMemories {
		limit = MaxProfileMemories
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT `+longTermMemoryColumns+` FROM user_long_term_memories WHERE username=$1 AND state='active' FOR UPDATE`, user)
	if err != nil {
		return nil, err
	}
	memories, err := scanLongTermMemories(rows)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ranked := RankMemories(memories, now)
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	for _, memory := range ranked {
		if _, err = tx.Exec(ctx, `UPDATE user_long_term_memories SET call_count=call_count+1,last_accessed_at=$2 WHERE id=$1`, memory.ID, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ranked, nil
}

func (p *MemoryPostgres) ActiveProfile(ctx context.Context, user string) ([]LongTermMemory, error) {
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT `+longTermMemoryColumns+` FROM user_long_term_memories WHERE username=$1 AND state='active' ORDER BY profile_slot`, user)
	if err != nil {
		return nil, err
	}
	return scanLongTermMemories(rows)
}

func (p *MemoryPostgres) ReplaceProfile(ctx context.Context, user string, drafts []MemoryDraft) ([]LongTermMemory, error) {
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	if len(drafts) > MaxProfileMemories {
		return nil, ErrTooManyProfileMemories
	}
	for _, draft := range drafts {
		if err := validateDraft(draft); err != nil {
			return nil, err
		}
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT `+longTermMemoryColumns+` FROM user_long_term_memories WHERE username=$1 AND state='active' FOR UPDATE`, user)
	if err != nil {
		return nil, err
	}
	currentRows, err := scanLongTermMemories(rows)
	if err != nil {
		return nil, err
	}
	active := map[string]LongTermMemory{}
	for _, memory := range currentRows {
		active[memory.ID] = memory
	}
	for _, draft := range drafts {
		if draft.ID == "" {
			continue
		}
		if _, ok := active[draft.ID]; !ok {
			return nil, ErrInvalidMemory
		}
	}
	now := time.Now().UTC()
	for _, draft := range drafts {
		if draft.ID == "" {
			continue
		}
		current := active[draft.ID]
		if !longTermIdentityChanged(current, draft) {
			continue
		}
		if err = insertSupersededSnapshot(ctx, tx, current, now); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE user_long_term_memories SET state='archived',profile_slot=NULL,archived_at=$2,updated_at=$2 WHERE username=$1 AND state='active'`, user, now); err != nil {
		return nil, err
	}
	for slot, draft := range drafts {
		tags, marshalErr := json.Marshal(draft.Tags)
		if marshalErr != nil {
			return nil, marshalErr
		}
		sessions, marshalErr := json.Marshal(draft.SourceSessions)
		if marshalErr != nil {
			return nil, marshalErr
		}
		turns, marshalErr := json.Marshal(draft.SourceTurnIDs)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if draft.ID != "" {
			_, err = tx.Exec(ctx, `UPDATE user_long_term_memories SET kind=$2,content=$3,tags=$4,importance=$5,confidence=$6,source_sessions=$7,source_turn_ids=$8,last_seen_at=$9,expires_at=$10,updated_at=$9,archived_at=NULL,profile_slot=$11,version=version+1,state='active' WHERE id=$1`, draft.ID, string(draft.Kind), draft.Content, tags, draft.Importance, draft.Confidence, sessions, turns, now, draft.ExpiresAt, slot+1)
		} else {
			id := uuid.NewString()
			_, err = tx.Exec(ctx, `INSERT INTO user_long_term_memories(id,username,kind,content,tags,importance,confidence,source_sessions,source_turn_ids,first_seen_at,last_seen_at,expires_at,profile_slot,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$11,$12,'active')`, id, user, string(draft.Kind), draft.Content, tags, draft.Importance, draft.Confidence, sessions, turns, now, draft.ExpiresAt, slot+1)
		}
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p.ActiveProfile(ctx, user)
}

func (p *MemoryPostgres) History(ctx context.Context, user, id string) ([]LongTermMemory, error) {
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT `+longTermMemoryColumns+` FROM user_long_term_memories WHERE username=$1 AND state='archived' AND superseded_by::text=$2 ORDER BY archived_at ASC, id::text ASC`, user, id)
	if err != nil {
		return nil, err
	}
	return scanLongTermMemories(rows)
}

func insertSupersededSnapshot(ctx context.Context, tx pgx.Tx, current LongTermMemory, now time.Time) error {
	tags, err := json.Marshal(current.Tags)
	if err != nil {
		return err
	}
	sessions, err := json.Marshal(current.SourceSessions)
	if err != nil {
		return err
	}
	turns, err := json.Marshal(current.SourceTurnIDs)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO user_long_term_memories(id,username,kind,content,tags,importance,confidence,source_sessions,source_turn_ids,call_count,first_seen_at,last_seen_at,last_accessed_at,expires_at,created_at,updated_at,archived_at,profile_slot,version,state,superseded_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16,NULL,$17,'archived',$18)`, uuid.NewString(), current.Username, string(current.Kind), current.Content, tags, current.Importance, current.Confidence, sessions, turns, current.CallCount, current.FirstSeenAt, current.LastSeenAt, current.LastAccessedAt, current.ExpiresAt, current.CreatedAt, now, current.Version, current.ID)
	return err
}

func (p *Postgres) Transcript(ctx context.Context, user string, after, through time.Time) ([]TranscriptTurn, error) {
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT t.session_id::text,t.id,t.status,t.messages,t.created_at FROM agent_turns t JOIN agent_sessions s ON s.id=t.session_id WHERE s.username=$1 AND t.status='completed' AND t.created_at>$2 AND t.created_at<=$3 ORDER BY t.created_at,t.id`, user, after, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TranscriptTurn{}
	for rows.Next() {
		var entry TranscriptTurn
		var data []byte
		if err = rows.Scan(&entry.SessionID, &entry.ID, &entry.Status, &data, &entry.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &entry.Messages); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (p *Postgres) UsersWithTranscript(ctx context.Context, after, through time.Time) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT s.username FROM agent_turns t JOIN agent_sessions s ON s.id=t.session_id WHERE t.status='completed' AND t.created_at>$1 AND t.created_at<=$2 ORDER BY s.username`, after, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var user string
		if err = rows.Scan(&user); err != nil {
			return nil, err
		}
		result = append(result, user)
	}
	return result, rows.Err()
}

func (p *MemoryPostgres) MemoryCheckpoint(ctx context.Context, user string) (time.Time, bool, error) {
	if err := ValidateUser(user); err != nil {
		return time.Time{}, false, err
	}
	var checkpoint time.Time
	err := p.pool.QueryRow(ctx, `SELECT processed_through FROM user_memory_checkpoints WHERE username=$1`, user).Scan(&checkpoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return checkpoint, err == nil, err
}

func (p *MemoryPostgres) SetMemoryCheckpoint(ctx context.Context, user string, through time.Time) error {
	if err := ValidateUser(user); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO user_memory_checkpoints(username,processed_through,updated_at) VALUES($1,$2,now()) ON CONFLICT(username) DO UPDATE SET processed_through=EXCLUDED.processed_through,updated_at=now()`, user, through.UTC())
	return err
}

func (p *MemoryPostgres) TryAcquireMemoryJob(ctx context.Context, user string) (MemoryJobLease, bool, error) {
	if err := ValidateUser(user); err != nil {
		return nil, false, err
	}
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	key := "user-memory:" + user
	var locked bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,1))", key).Scan(&locked)
	if err != nil || !locked {
		conn.Release()
		return nil, false, err
	}
	return &postgresMemoryJobLease{conn: conn, key: key}, true, nil
}

type postgresMemoryJobLease struct {
	conn *pgxpool.Conn
	key  string
}

func (l *postgresMemoryJobLease) Close() {
	if l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.conn.Exec(ctx, "SELECT pg_advisory_unlock(hashtextextended($1,1))", l.key); err != nil {
		_ = l.conn.Conn().Close(ctx)
	}
	l.conn.Release()
	l.conn = nil
}

type longTermRow interface {
	Scan(...any) error
}

func scanLongTermMemories(rows pgx.Rows) ([]LongTermMemory, error) {
	defer rows.Close()
	result := []LongTermMemory{}
	for rows.Next() {
		memory, err := scanLongTermMemory(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, memory)
	}
	return result, rows.Err()
}

func scanLongTermMemory(row longTermRow) (LongTermMemory, error) {
	var memory LongTermMemory
	var kind string
	var tags, sessions, turnIDs []byte
	if err := row.Scan(&memory.ID, &memory.Username, &kind, &memory.Content, &tags, &memory.Importance, &memory.Confidence, &sessions, &turnIDs, &memory.CallCount, &memory.FirstSeenAt, &memory.LastSeenAt, &memory.LastAccessedAt, &memory.ExpiresAt, &memory.CreatedAt, &memory.UpdatedAt, &memory.ArchivedAt, &memory.ProfileSlot, &memory.Version, &memory.State, &memory.SupersededBy); err != nil {
		return LongTermMemory{}, err
	}
	memory.Kind = MemoryKind(kind)
	if err := json.Unmarshal(tags, &memory.Tags); err != nil {
		return LongTermMemory{}, fmt.Errorf("decode memory tags: %w", err)
	}
	if err := json.Unmarshal(sessions, &memory.SourceSessions); err != nil {
		return LongTermMemory{}, fmt.Errorf("decode memory source sessions: %w", err)
	}
	if err := json.Unmarshal(turnIDs, &memory.SourceTurnIDs); err != nil {
		return LongTermMemory{}, fmt.Errorf("decode memory source turns: %w", err)
	}
	return memory, nil
}
