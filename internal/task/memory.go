package task

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Memory struct {
	mu        sync.Mutex
	tasks     map[string]Task
	outbox    map[string]Notification
	delivered map[string]bool
}

func NewMemory() *Memory {
	return &Memory{tasks: map[string]Task{}, outbox: map[string]Notification{}, delivered: map[string]bool{}}
}
func (m *Memory) put(t Task) {
	m.tasks[t.ID] = clone(t)
	if terminal(t.Status) && !m.delivered[t.NotificationID()] {
		if _, exists := m.outbox[t.NotificationID()]; exists {
			return
		}
		m.outbox[t.NotificationID()] = Notification{t.NotificationID(), t.Owner, t.Notification()}
	}
}
func (m *Memory) Create(ctx context.Context, o Owner, b Brief) (Task, error) {
	if err := ctx.Err(); err != nil {
		return Task{}, err
	}
	t, err := newTask(o, b)
	if err != nil {
		return Task{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, old := range m.tasks {
		if old.Owner == o && old.Brief.Key == b.Key {
			if old.RequestFingerprint != briefFingerprint(b) {
				return Task{}, ErrConflict
			}
			return clone(old), nil
		}
	}
	m.put(t)
	return clone(t), nil
}
func (m *Memory) Get(ctx context.Context, o Owner, id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || !authorized(t, o) {
		return Task{}, ErrNotFound
	}
	return clone(t), ctx.Err()
}
func (m *Memory) List(ctx context.Context, o Owner) ([]Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Task{}
	for _, t := range m.tasks {
		if authorized(t, o) {
			out = append(out, clone(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, ctx.Err()
}
func (m *Memory) Claim(ctx context.Context, ttl time.Duration) (Task, error) {
	if err := ctx.Err(); err != nil {
		return Task{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	var chosen *Task
	for _, t := range m.tasks {
		if t.Status == Queued || (t.Status == Running && !t.LeaseUntil.After(now)) {
			if chosen == nil || t.CreatedAt.Before(chosen.CreatedAt) {
				c := t
				chosen = &c
			}
		}
	}
	if chosen == nil {
		return Task{}, ErrNoTask
	}
	chosen.Status = Running
	chosen.Token = uuid.NewString()
	chosen.LeaseUntil = now.Add(ttl)
	chosen.Event("running", "worker claimed execution")
	m.put(*chosen)
	return clone(*chosen), nil
}
func (m *Memory) Save(ctx context.Context, t Task, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.tasks[t.ID]
	if !ok || old.Status != Running || old.Token != token || !old.LeaseUntil.After(time.Now()) {
		return ErrLeaseLost
	}
	t.Token = token
	t.LeaseUntil = old.LeaseUntil
	m.put(t)
	return nil
}
func (m *Memory) Heartbeat(ctx context.Context, id, token string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.Status != Running || t.Token != token || !t.LeaseUntil.After(time.Now()) {
		return ErrLeaseLost
	}
	t.LeaseUntil = time.Now().UTC().Add(ttl)
	m.tasks[id] = t
	return nil
}
func (m *Memory) Release(ctx context.Context, id, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.Status != Running || t.Token != token {
		return ErrLeaseLost
	}
	t.Status = Queued
	t.Token = ""
	t.LeaseUntil = time.Time{}
	m.tasks[id] = t
	return ctx.Err()
}

// AppendEvent records history for a worker that still holds the claim token.
// Status and the notification outbox stay as they are.
func (m *Memory) AppendEvent(ctx context.Context, id, token, kind, detail string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if token == "" {
		return ErrLeaseLost
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.Token != token {
		return ErrLeaseLost
	}
	t.Event(kind, detail)
	m.tasks[id] = clone(t)
	return nil
}
func (m *Memory) change(ctx context.Context, o Owner, id string, fn func(*Task) error) (Task, error) {
	if err := ctx.Err(); err != nil {
		return Task{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || !authorized(t, o) {
		return Task{}, ErrNotFound
	}
	t = clone(t)
	if err := fn(&t); err != nil {
		return Task{}, err
	}
	m.put(t)
	return clone(t), nil
}
func cancel(t *Task) error {
	if t.Status == Completed || t.Status == Canceled {
		return nil
	}
	t.Status = Canceled
	t.Token = ""
	t.Event("canceled", "canceled by owner")
	return nil
}
func (m *Memory) Cancel(ctx context.Context, o Owner, id string) (Task, error) {
	return m.change(ctx, o, id, cancel)
}
func (m *Memory) Resume(ctx context.Context, o Owner, id string, in Resume) (Task, error) {
	return m.change(ctx, o, id, func(t *Task) error { return resume(t, in) })
}
func (m *Memory) UpdatePlan(ctx context.Context, o Owner, id string, p []PlanStep) (Task, error) {
	return m.change(ctx, o, id, func(t *Task) error {
		if t.Status == Running {
			return ErrBusy
		}
		t.Plan = p
		t.Event("plan", "plan updated")
		return nil
	})
}
func (m *Memory) Pending(ctx context.Context) ([]Notification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Notification{}
	for _, n := range m.outbox {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, ctx.Err()
}
func (m *Memory) MarkEnqueued(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	m.delivered[id] = true
	delete(m.outbox, id)
	return ctx.Err()
}
