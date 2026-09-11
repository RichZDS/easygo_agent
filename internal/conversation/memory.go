package conversation

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type memoryRun struct {
	record RunRecord
}

type memoryEntry struct {
	session  Session
	messages []*schema.AgenticMessage
	turns    []Turn
	runs     []*memoryRun
	busy     bool
}
type Memory struct {
	mu          sync.Mutex
	entries     map[string]*memoryEntry
	longTerms   map[string]map[string]LongTermMemory
	checkpoints map[string]time.Time
	memoryJobs  map[string]bool
	maxPending  int
}

func NewMemory() *Memory { return NewMemoryWithQueueLimit(DefaultMaxPendingRuns) }

func NewMemoryWithQueueLimit(maxPending int) *Memory {
	if maxPending < 1 {
		maxPending = DefaultMaxPendingRuns
	}
	return &Memory{
		entries:     map[string]*memoryEntry{},
		longTerms:   map[string]map[string]LongTermMemory{},
		checkpoints: map[string]time.Time{},
		memoryJobs:  map[string]bool{},
		maxPending:  maxPending,
	}
}

func (m *Memory) SetMaxPendingRuns(maxPending int) {
	if maxPending < 1 {
		maxPending = DefaultMaxPendingRuns
	}
	m.mu.Lock()
	m.maxPending = maxPending
	m.mu.Unlock()
}
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
	if err := ValidateUser(user); err != nil {
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
	if err := ValidateUser(user); err != nil {
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
	if err := ValidateUser(user); err != nil {
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
	for _, run := range e.runs {
		if run.record.Status == RunQueued || run.record.Status == RunRunning {
			return nil, ErrBusy
		}
	}
	messages, err := Clone(e.messages)
	if err != nil {
		return nil, err
	}
	e.busy = true
	return &memoryLease{store: m, entry: e, messages: messages}, nil
}

func (m *Memory) Enqueue(ctx context.Context, user, sessionID, input, idempotencyKey string) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	if err := ValidateUser(user); err != nil {
		return RunRecord{}, err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return RunRecord{}, ErrEmptyInput
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[sessionID]
	if e == nil || e.session.Username != user {
		return RunRecord{}, ErrNotFound
	}
	for _, queued := range e.runs {
		if idempotencyKey != "" && queued.record.IdempotencyKey == idempotencyKey {
			if queued.record.Input != input {
				return RunRecord{}, ErrIdempotencyConflict
			}
			return m.withPositionLocked(e, queued.record), nil
		}
	}
	queuedCount := 0
	for _, queued := range e.runs {
		if queued.record.Status == RunQueued {
			queuedCount++
		}
	}
	if queuedCount >= m.maxPending {
		return RunRecord{}, ErrQueueFull
	}
	now := time.Now().UTC()
	for _, queued := range e.runs {
		if !now.After(queued.record.CreatedAt) {
			now = queued.record.CreatedAt.Add(time.Nanosecond)
		}
	}
	run := &memoryRun{record: RunRecord{
		ID:             uuid.NewString(),
		SessionID:      sessionID,
		Input:          input,
		Status:         RunQueued,
		IdempotencyKey: idempotencyKey,
		CreatedAt:      now,
	}}
	e.runs = append(e.runs, run)
	e.session.UpdatedAt = now
	return m.withPositionLocked(e, run.record), nil
}

func (m *Memory) GetRun(ctx context.Context, user, sessionID, runID string) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	if err := ValidateUser(user); err != nil {
		return RunRecord{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[sessionID]
	if e == nil || e.session.Username != user {
		return RunRecord{}, ErrNotFound
	}
	for _, run := range e.runs {
		if run.record.ID == runID {
			return m.withPositionLocked(e, run.record), nil
		}
	}
	return RunRecord{}, ErrNotFound
}

func (m *Memory) ListRuns(ctx context.Context, user, sessionID string, limit int) ([]RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[sessionID]
	if e == nil || e.session.Username != user {
		return nil, ErrNotFound
	}
	runs := make([]*memoryRun, 0, len(e.runs))
	for _, run := range e.runs {
		if run.record.Status == RunQueued || run.record.Status == RunRunning {
			runs = append(runs, run)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].record.CreatedAt.Equal(runs[j].record.CreatedAt) {
			return runs[i].record.ID < runs[j].record.ID
		}
		return runs[i].record.CreatedAt.Before(runs[j].record.CreatedAt)
	})
	limit = pageSize(limit)
	if len(runs) > limit {
		runs = runs[:limit]
	}
	result := make([]RunRecord, 0, len(runs))
	for _, run := range runs {
		result = append(result, m.withPositionLocked(e, run.record))
	}
	return result, nil
}

func (m *Memory) RequestCancel(ctx context.Context, user, sessionID, runID string) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	if err := ValidateUser(user); err != nil {
		return RunRecord{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[sessionID]
	if e == nil || e.session.Username != user {
		return RunRecord{}, ErrNotFound
	}
	for _, run := range e.runs {
		if run.record.ID != runID {
			continue
		}
		switch run.record.Status {
		case RunQueued:
			now := time.Now().UTC()
			run.record.Status = RunCanceled
			run.record.FinishedAt = &now
			e.session.UpdatedAt = now
		case RunRunning:
			run.record.CancelRequested = true
			e.session.UpdatedAt = time.Now().UTC()
		}
		return m.withPositionLocked(e, run.record), nil
	}
	return RunRecord{}, ErrNotFound
}

func (m *Memory) ClaimNext(ctx context.Context, workerID string, leaseTTL time.Duration) (RunLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workerID) == "" {
		return nil, errors.New("worker ID cannot be empty")
	}
	if leaseTTL <= 0 {
		leaseTTL = 30 * time.Second
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var selected *memoryRun
	var selectedEntry *memoryEntry
	for _, entry := range m.entries {
		if entry.busy {
			continue
		}
		for _, run := range entry.runs {
			if run.record.Status != RunQueued {
				continue
			}
			if selected == nil || run.record.CreatedAt.Before(selected.record.CreatedAt) ||
				(run.record.CreatedAt.Equal(selected.record.CreatedAt) && run.record.ID < selected.record.ID) {
				selected, selectedEntry = run, entry
			}
		}
	}
	if selected == nil {
		return nil, ErrNoQueuedRun
	}
	messages, err := Clone(selectedEntry.messages)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	selected.record.Status = RunRunning
	selected.record.WorkerID = workerID
	selected.record.ClaimToken = uuid.NewString()
	selected.record.LeaseExpiresAt = now.Add(leaseTTL)
	selected.record.StartedAt = &now
	selectedEntry.busy = true
	return &memoryRunLease{store: m, entry: selectedEntry, run: selected, claimToken: selected.record.ClaimToken, messages: messages}, nil
}

func (m *Memory) RecoverExpired(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.entries {
		for _, run := range entry.runs {
			if run.record.Status != RunRunning || run.record.LeaseExpiresAt.IsZero() || run.record.LeaseExpiresAt.After(now) {
				continue
			}
			if run.record.CancelRequested {
				run.record.Status = RunCanceled
				run.record.FinishedAt = &now
				entry.session.UpdatedAt = now
			} else {
				run.record.Status = RunQueued
				run.record.StartedAt = nil
			}
			run.record.WorkerID = ""
			run.record.ClaimToken = ""
			run.record.LeaseExpiresAt = time.Time{}
			entry.busy = false
		}
	}
	return nil
}

func (m *Memory) withPositionLocked(entry *memoryEntry, record RunRecord) RunRecord {
	record = CloneRunRecord(record)
	if entry != nil {
		record.Username = entry.session.Username
	}
	record.Position = 0
	if record.Status != RunQueued {
		return record
	}
	for _, run := range entry.runs {
		if run.record.Status != RunQueued {
			continue
		}
		if run.record.CreatedAt.Before(record.CreatedAt) ||
			(run.record.CreatedAt.Equal(record.CreatedAt) && run.record.ID < record.ID) {
			record.Position++
		}
	}
	record.Position++
	return record
}

type memoryLease struct {
	store     *Memory
	entry     *memoryEntry
	messages  []*schema.AgenticMessage
	closed    bool
	committed bool
}

func (l *memoryLease) Messages() []*schema.AgenticMessage {
	result, err := Clone(l.messages)
	if err != nil {
		return nil
	}
	return result
}
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

type memoryRunLease struct {
	store      *Memory
	entry      *memoryEntry
	run        *memoryRun
	messages   []*schema.AgenticMessage
	closed     bool
	committed  bool
	claimToken string
}

func (l *memoryRunLease) Run() RunRecord {
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	record := CloneRunRecord(l.run.record)
	record.Username = l.entry.session.Username
	return record
}

func (l *memoryRunLease) Messages() []*schema.AgenticMessage {
	result, err := Clone(l.messages)
	if err != nil {
		return nil
	}
	return result
}

func (l *memoryRunLease) CommitRun(ctx context.Context, nextMessages []*schema.AgenticMessage, status RunStatus, outputs []*schema.AgenticMessage, resultText, runError string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if status != RunCompleted && status != RunFailed && status != RunCanceled {
		return ErrInvalidRunStatus
	}
	next, err := Clone(nextMessages)
	if err != nil {
		return err
	}
	audit, err := runAudit(l.run.record.Input, outputs)
	if err != nil {
		return err
	}
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	if l.closed || l.committed {
		return ErrBusy
	}
	if l.run.record.Status != RunRunning || l.run.record.ClaimToken != l.claimToken {
		return ErrLeaseLost
	}
	if status == RunCompleted && l.run.record.CancelRequested {
		return ErrCancellationRequested
	}
	now := time.Now().UTC()
	turn := Turn{ID: int64(len(l.entry.turns) + 1), Status: string(status), Messages: audit, CreatedAt: now}
	l.entry.turns = append(l.entry.turns, turn)
	l.entry.messages = next
	l.entry.session.UpdatedAt = now
	l.run.record.Status = status
	if status != RunCanceled {
		l.run.record.CancelRequested = false
	}
	l.run.record.ResultText = resultText
	l.run.record.Error = runError
	l.run.record.TurnID = turn.ID
	l.run.record.FinishedAt = &now
	l.run.record.WorkerID = ""
	l.run.record.ClaimToken = ""
	l.run.record.LeaseExpiresAt = time.Time{}
	l.committed = true
	return nil
}

func (l *memoryRunLease) CancelRequested(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	if l.closed {
		return false, ErrBusy
	}
	if l.run.record.Status != RunRunning || l.run.record.ClaimToken != l.claimToken {
		return false, ErrLeaseLost
	}
	return l.run.record.CancelRequested, nil
}

func (l *memoryRunLease) Heartbeat(ctx context.Context, leaseTTL time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	if l.closed {
		return ErrBusy
	}
	if l.run.record.Status != RunRunning || l.run.record.ClaimToken != l.claimToken {
		return ErrLeaseLost
	}
	if leaseTTL <= 0 {
		leaseTTL = 30 * time.Second
	}
	l.run.record.LeaseExpiresAt = time.Now().UTC().Add(leaseTTL)
	return nil
}

func (l *memoryRunLease) Close() {
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	if l.closed {
		return
	}
	ownsLease := l.run.record.Status == RunRunning && l.run.record.ClaimToken == l.claimToken
	if !l.committed && ownsLease {
		if l.run.record.CancelRequested {
			now := time.Now().UTC()
			l.run.record.Status = RunCanceled
			l.run.record.FinishedAt = &now
		} else {
			l.run.record.Status = RunQueued
			l.run.record.StartedAt = nil
		}
		l.run.record.WorkerID = ""
		l.run.record.ClaimToken = ""
		l.run.record.LeaseExpiresAt = time.Time{}
	}
	if ownsLease || l.committed {
		l.entry.busy = false
	}
	l.closed = true
}

var _ QueueStore = (*Memory)(nil)
var _ MemoryStore = (*Memory)(nil)
var _ RunLease = (*memoryRunLease)(nil)
