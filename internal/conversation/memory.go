package conversation

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"sort"
	"sync"
	"time"
)

type memoryEntry struct {
	session  Session
	messages []*schema.AgenticMessage
	turns    []Turn
	busy     bool
}
type Memory struct {
	mu      sync.Mutex
	entries map[string]*memoryEntry
}

func NewMemory() *Memory { return &Memory{entries: map[string]*memoryEntry{}} }
func (m *Memory) Close() {}
func (m *Memory) Create(ctx context.Context, user string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if err := ValidateUser(user); err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	s := Session{ID: uuid.NewString(), Username: user, CreatedAt: now, UpdatedAt: now}
	m.entries[s.ID] = &memoryEntry{session: s}
	return s, nil
}
func (m *Memory) List(ctx context.Context, user string, limit, offset int) ([]Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []Session{}
	for _, e := range m.entries {
		if e.session.Username == user {
			result = append(result, e.session)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	offset = min(max(offset, 0), len(result))
	return result[offset:min(offset+pageSize(limit), len(result))], nil
}
func (m *Memory) History(ctx context.Context, user, id string, after int64, limit int) ([]Turn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[id]
	if e == nil || e.session.Username != user {
		return nil, ErrNotFound
	}
	result := []Turn{}
	for _, turn := range e.turns {
		if turn.ID > after {
			copy := turn
			var err error
			copy.Messages, err = Clone(turn.Messages)
			if err != nil {
				return nil, err
			}
			result = append(result, copy)
			if len(result) == pageSize(limit) {
				break
			}
		}
	}
	return result, nil
}
func (m *Memory) Begin(ctx context.Context, user, id string) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[id]
	if e == nil || e.session.Username != user {
		return nil, ErrNotFound
	}
	if e.busy {
		return nil, ErrBusy
	}
	messages, err := Clone(e.messages)
	if err != nil {
		return nil, err
	}
	e.busy = true
	return &memoryLease{store: m, entry: e, messages: messages}, nil
}

type memoryLease struct {
	store     *Memory
	entry     *memoryEntry
	messages  []*schema.AgenticMessage
	closed    bool
	committed bool
}

func (l *memoryLease) Messages() []*schema.AgenticMessage { return l.messages }
func (l *memoryLease) Commit(ctx context.Context, messages []*schema.AgenticMessage, turn Turn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	next, err := Clone(messages)
	if err != nil {
		return err
	}
	audit, err := Clone(turn.Messages)
	if err != nil {
		return err
	}
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	if l.closed || l.committed {
		return ErrBusy
	}
	turn.Messages = audit
	turn.ID = int64(len(l.entry.turns) + 1)
	turn.CreatedAt = time.Now().UTC()
	l.entry.turns = append(l.entry.turns, turn)
	l.entry.messages = next
	l.entry.session.UpdatedAt = turn.CreatedAt
	l.committed = true
	return nil
}
func (l *memoryLease) Close() {
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	if !l.closed {
		l.entry.busy = false
		l.closed = true
	}
}
