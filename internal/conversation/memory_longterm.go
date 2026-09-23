package conversation

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryLongTerm is the in-memory MemoryStore adapter. It is a different
// object from the conversation/queue store so recall never shares that lock.
type MemoryLongTerm struct {
	mu          sync.Mutex
	longTerms   map[string]map[string]LongTermMemory
	checkpoints map[string]time.Time
	memoryJobs  map[string]bool
}

func NewMemoryLongTerm() *MemoryLongTerm {
	return &MemoryLongTerm{
		longTerms:   map[string]map[string]LongTermMemory{},
		checkpoints: map[string]time.Time{},
		memoryJobs:  map[string]bool{},
	}
}

func (m *MemoryLongTerm) Close() {}

func (m *MemoryLongTerm) Recall(ctx context.Context, user string, limit int) ([]LongTermMemory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	memories := make([]LongTermMemory, 0, len(m.longTerms[user]))
	for _, memory := range m.longTerms[user] {
		memories = append(memories, cloneLongTermMemory(memory))
	}
	ranked := RankMemories(memories, now)
	if limit < 1 || limit > MaxProfileMemories {
		limit = MaxProfileMemories
	}
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	for i := range ranked {
		stored := m.longTerms[user][ranked[i].ID]
		stored.CallCount++
		stored.LastAccessedAt = &now
		m.longTerms[user][ranked[i].ID] = stored
	}
	return ranked, nil
}

func (m *MemoryLongTerm) ActiveProfile(ctx context.Context, user string) ([]LongTermMemory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []LongTermMemory{}
	for _, memory := range m.longTerms[user] {
		if memory.State == memoryStateActive {
			result = append(result, cloneLongTermMemory(memory))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ProfileSlot < result[j].ProfileSlot })
	return result, nil
}

func (m *MemoryLongTerm) ReplaceProfile(ctx context.Context, user string, drafts []MemoryDraft) ([]LongTermMemory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.longTerms[user] == nil {
		m.longTerms[user] = map[string]LongTermMemory{}
	}
	now := time.Now().UTC()
	selected := map[string]bool{}
	result := make([]LongTermMemory, 0, len(drafts))
	for slot, draft := range drafts {
		memory, exists := m.longTerms[user][draft.ID]
		if draft.ID != "" && !exists {
			return nil, ErrInvalidMemory
		}
		if exists && longTermIdentityChanged(memory, draft) {
			snapshot := archiveSuperseded(memory, now)
			m.longTerms[user][snapshot.ID] = snapshot
		}
		if !exists {
			memory = LongTermMemory{ID: uuid.NewString(), Username: user, FirstSeenAt: now, CreatedAt: now, Version: 1}
		} else {
			memory.Version++
		}
		memory.Kind = draft.Kind
		memory.Content = draft.Content
		memory.Tags = append([]string(nil), draft.Tags...)
		memory.Importance = draft.Importance
		memory.Confidence = draft.Confidence
		memory.SourceSessions = append([]string(nil), draft.SourceSessions...)
		memory.SourceTurnIDs = append([]int64(nil), draft.SourceTurnIDs...)
		memory.ExpiresAt = draft.ExpiresAt
		memory.LastSeenAt = now
		memory.UpdatedAt = now
		memory.ArchivedAt = nil
		memory.ProfileSlot = slot + 1
		memory.State = memoryStateActive
		memory.SupersededBy = nil
		m.longTerms[user][memory.ID] = memory
		selected[memory.ID] = true
		result = append(result, cloneLongTermMemory(memory))
	}
	for id, memory := range m.longTerms[user] {
		if memory.State == memoryStateActive && !selected[id] {
			memory.State = memoryStateArchive
			memory.ArchivedAt = &now
			memory.UpdatedAt = now
			memory.ProfileSlot = 0
			memory.SupersededBy = nil
			m.longTerms[user][id] = memory
		}
	}
	return result, nil
}

func (m *MemoryLongTerm) History(ctx context.Context, user, id string) ([]LongTermMemory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []LongTermMemory{}
	for _, memory := range m.longTerms[user] {
		if memory.State != memoryStateArchive || memory.SupersededBy == nil || *memory.SupersededBy != id {
			continue
		}
		result = append(result, cloneLongTermMemory(memory))
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := time.Time{}, time.Time{}
		if result[i].ArchivedAt != nil {
			left = *result[i].ArchivedAt
		}
		if result[j].ArchivedAt != nil {
			right = *result[j].ArchivedAt
		}
		if !left.Equal(right) {
			return left.Before(right)
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func archiveSuperseded(current LongTermMemory, now time.Time) LongTermMemory {
	snapshot := cloneLongTermMemory(current)
	survivingID := current.ID
	snapshot.ID = uuid.NewString()
	snapshot.ProfileSlot = 0
	snapshot.State = memoryStateArchive
	archivedAt := now
	snapshot.ArchivedAt = &archivedAt
	snapshot.UpdatedAt = now
	snapshot.SupersededBy = &survivingID
	return snapshot
}

func (m *MemoryLongTerm) MemoryCheckpoint(ctx context.Context, user string) (time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.checkpoints[user]
	return value, ok, nil
}

func (m *MemoryLongTerm) SetMemoryCheckpoint(ctx context.Context, user string, through time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateUser(user); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkpoints[user] = through.UTC()
	return nil
}

func (m *MemoryLongTerm) TryAcquireMemoryJob(ctx context.Context, user string) (MemoryJobLease, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := ValidateUser(user); err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.memoryJobs[user] {
		return nil, false, nil
	}
	m.memoryJobs[user] = true
	return &memoryLongTermJobLease{store: m, user: user}, true, nil
}

type memoryLongTermJobLease struct {
	store *MemoryLongTerm
	user  string
}

func (l *memoryLongTermJobLease) Close() {
	if l.store == nil {
		return
	}
	l.store.mu.Lock()
	l.store.memoryJobs[l.user] = false
	l.store.mu.Unlock()
	l.store = nil
}

var _ MemoryStore = (*MemoryLongTerm)(nil)
