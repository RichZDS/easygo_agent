package conversation

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
)

func (m *Memory) Recall(ctx context.Context, user string, limit int) ([]LongTermMemory, error) {
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

func (m *Memory) ActiveProfile(ctx context.Context, user string) ([]LongTermMemory, error) {
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

func (m *Memory) ReplaceProfile(ctx context.Context, user string, drafts []MemoryDraft) ([]LongTermMemory, error) {
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
			m.longTerms[user][id] = memory
		}
	}
	return result, nil
}

func (m *Memory) Transcript(ctx context.Context, user string, after, through time.Time) ([]TranscriptTurn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []TranscriptTurn{}
	for id, entry := range m.entries {
		if entry.session.Username != user {
			continue
		}
		for _, turn := range entry.turns {
			if turn.Status != "completed" || !turn.CreatedAt.After(after) || turn.CreatedAt.After(through) {
				continue
			}
			copy := turn
			var err error
			copy.Messages, err = Clone(turn.Messages)
			if err != nil {
				return nil, err
			}
			result = append(result, TranscriptTurn{SessionID: id, Turn: copy})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func (m *Memory) UsersWithTranscript(ctx context.Context, after, through time.Time) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	users := map[string]struct{}{}
	for _, entry := range m.entries {
		for _, turn := range entry.turns {
			if turn.Status == "completed" && turn.CreatedAt.After(after) && !turn.CreatedAt.After(through) {
				users[entry.session.Username] = struct{}{}
				break
			}
		}
	}
	result := make([]string, 0, len(users))
	for user := range users {
		result = append(result, user)
	}
	sort.Strings(result)
	return result, nil
}

func (m *Memory) MemoryCheckpoint(ctx context.Context, user string) (time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.checkpoints[user]
	return value, ok, nil
}

func (m *Memory) SetMemoryCheckpoint(ctx context.Context, user string, through time.Time) error {
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

func (m *Memory) TryAcquireMemoryJob(ctx context.Context, user string) (MemoryJobLease, bool, error) {
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
	return &memoryJobLease{store: m, user: user}, true, nil
}

type memoryJobLease struct {
	store *Memory
	user  string
}

func (l *memoryJobLease) Close() {
	if l.store == nil {
		return
	}
	l.store.mu.Lock()
	l.store.memoryJobs[l.user] = false
	l.store.mu.Unlock()
	l.store = nil
}
