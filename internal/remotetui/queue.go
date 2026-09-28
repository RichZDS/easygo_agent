package remotetui

import (
	"context"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/conversation"
	"errors"
	"github.com/google/uuid"
	"sort"
	"sync"
	"time"
)

type WireRun struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"session_id"`
	Status         string    `json:"status"`
	Input          string    `json:"input"`
	InputTruncated bool      `json:"input_truncated"`
	CreatedAt      time.Time `json:"created_at"`
	Result         *struct {
		Message Message `json:"message"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (w WireRun) record() conversation.RunRecord {
	r := conversation.RunRecord{ID: w.ID, SessionID: w.SessionID, Status: conversation.RunStatus(w.Status), CreatedAt: w.CreatedAt, Input: w.Input}
	if w.Status == "interrupted" {
		r.Status = conversation.RunFailed
		r.Error = "remote run interrupted"
	}
	if w.Result != nil {
		for _, b := range w.Result.Message.Content {
			if b.Type == "text" {
				r.ResultText += b.Text
			}
		}
	}
	if w.Error != nil {
		r.Error = w.Error.Message
	}
	return r
}

type Queue struct {
	client  *Client
	runtime string
	poll    time.Duration
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	known   map[string]conversation.RunRecord
	closed  bool
	wg      sync.WaitGroup
}

func NewQueue(c *Client, runtime string) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	return &Queue{client: c, runtime: runtime, poll: 500 * time.Millisecond, ctx: ctx, cancel: cancel, known: map[string]conversation.RunRecord{}}
}
func (q *Queue) remember(w WireRun) conversation.RunRecord {
	r := w.record()
	q.mu.Lock()
	q.known[r.ID] = r
	q.mu.Unlock()
	return r
}
func (q *Queue) Submit(ctx context.Context, user, session, input, key string) (conversation.RunRecord, agentruntime.RunHandle, error) {
	q.mu.Lock()
	closed := q.closed
	q.mu.Unlock()
	if closed {
		return conversation.RunRecord{}, nil, agentruntime.ErrQueueClosed
	}
	if key == "" {
		key = uuid.NewString()
	}
	p := map[string]any{"session_id": session, "input": input, "idempotency_key": key}
	if q.runtime != "" {
		p["workshop_runtime"] = q.runtime
	}
	var w WireRun
	err := q.client.RPC(ctx, "agent.run.start", p, &w)
	if err != nil {
		return conversation.RunRecord{}, nil, err
	}
	if w.ID == "" || w.SessionID != session {
		return conversation.RunRecord{}, nil, errors.New("invalid remote run receipt")
	}
	r := q.remember(w)
	r.Input = input
	return r, &handle{q: q, user: user, session: session, record: r}, nil
}
func (q *Queue) Get(ctx context.Context, _ string, session, id string) (conversation.RunRecord, error) {
	var w WireRun
	err := q.client.RPC(ctx, "agent.run.get", map[string]any{"run_id": id}, &w)
	if err != nil {
		return conversation.RunRecord{}, err
	}
	if w.ID != id || w.SessionID != session {
		return conversation.RunRecord{}, errors.New("remote run identity mismatch")
	}
	return q.remember(w), nil
}
func (q *Queue) List(ctx context.Context, _ string, session string, limit int) ([]conversation.RunRecord, error) {
	h, err := q.client.History(ctx, session, 0)
	if err != nil {
		return nil, err
	}
	for _, w := range h.Runs {
		if w.SessionID != session {
			return nil, errors.New("remote session identity mismatch")
		}
		q.remember(w)
	}
	q.mu.Lock()
	ids := []string{}
	for id, r := range q.known {
		if r.SessionID == session && (r.Status == conversation.RunQueued || r.Status == conversation.RunRunning) {
			ids = append(ids, id)
		}
	}
	q.mu.Unlock()
	sort.Strings(ids)
	if limit < 1 || limit > 100 {
		limit = 100
	}
	runs := []conversation.RunRecord{}
	for _, id := range ids {
		r, e := q.Get(ctx, "", session, id)
		if e != nil {
			return nil, e
		}
		if r.Status == conversation.RunQueued || r.Status == conversation.RunRunning {
			runs = append(runs, r)
		}
		if len(runs) >= limit {
			break
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].CreatedAt.Before(runs[j].CreatedAt) })
	return runs, nil
}
func (q *Queue) Cancel(ctx context.Context, _ string, session, id string) (conversation.RunRecord, error) {
	// Validate the session before mutation, though server authentication is the ownership authority.
	if _, err := q.Get(ctx, "", session, id); err != nil {
		return conversation.RunRecord{}, err
	}
	var w WireRun
	err := q.client.RPC(ctx, "agent.run.cancel", map[string]any{"run_id": id}, &w)
	if err != nil {
		return conversation.RunRecord{}, err
	}
	if w.ID != id || w.SessionID != session {
		return conversation.RunRecord{}, errors.New("invalid cancellation receipt")
	}
	return q.remember(w), nil
}
func (q *Queue) Close() error {
	q.mu.Lock()
	q.closed = true
	q.cancel()
	q.mu.Unlock()
	q.wg.Wait()
	return nil
}

type handle struct {
	q             *Queue
	user, session string
	record        conversation.RunRecord
}

func (h *handle) Run() conversation.RunRecord { return h.record }
func (h *handle) Cancel()                     { _, _ = h.q.Cancel(context.Background(), h.user, h.session, h.record.ID) }
func (h *handle) Close()                      {}

type subscription struct {
	events chan agentruntime.Event
	cancel context.CancelFunc
}

func (s *subscription) Events() <-chan agentruntime.Event { return s.events }
func (s *subscription) Close()                            { s.cancel() }
func (q *Queue) Subscribe(ctx context.Context, _ string, session, id string) (agentruntime.Subscription, error) {
	if _, err := q.Get(ctx, "", session, id); err != nil {
		return nil, err
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil, agentruntime.ErrQueueClosed
	}
	q.wg.Add(1)
	q.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(q.ctx, cancel)
	s := &subscription{events: make(chan agentruntime.Event, 32), cancel: cancel}
	go func() {
		defer q.wg.Done()
		defer stop()
		defer cancel()
		defer close(s.events)
		send := func(e agentruntime.Event) bool {
			select {
			case s.events <- e:
				return true
			case <-child.Done():
				return false
			}
		}
		var after int64
		lastStatus := conversation.RunStatus("")
		failures := 0
		for {
			if child.Err() != nil {
				return
			}
			var page struct {
				Events []struct {
					Seq  int64  `json:"seq"`
					Kind string `json:"kind"`
					Data struct {
						Event struct {
							Type  string `json:"type"`
							Delta string `json:"delta"`
						} `json:"event"`
					} `json:"data"`
				} `json:"events"`
				NextAfter *int64 `json:"next_after"`
			}
			err := q.client.RPC(child, "agent.run.events", map[string]any{"run_id": id, "after": after, "limit": 1000}, &page)
			if err == nil {
				for _, e := range page.Events {
					if e.Seq <= after {
						continue
					}
					after = e.Seq
					if e.Kind == "delta" {
						kind := agentruntime.EventTextDelta
						if e.Data.Event.Type == "reasoning_delta" {
							kind = agentruntime.EventReasoningDelta
						} else if e.Data.Event.Type != "text_delta" {
							continue
						}
						if !send(agentruntime.Event{Kind: kind, RunID: id, Text: e.Data.Event.Delta}) {
							return
						}
					}
				}
			}
			var r conversation.RunRecord
			if err == nil {
				r, err = q.Get(child, "", session, id)
			}
			if err != nil {
				failures++
				var status *HTTPError
				if errors.As(err, &status) && (status.Status == 401 || status.Status == 403) || failures >= 20 {
					send(agentruntime.Event{Kind: agentruntime.EventFailed, RunID: id, Err: errors.New("remote connection lost; reconnect with the same session (run was not canceled)")})
					return
				}
			} else {
				failures = 0
				kind := agentruntime.EventKind(r.Status)
				if r.Status != lastStatus || kind == agentruntime.EventCompleted || kind == agentruntime.EventCanceled || kind == agentruntime.EventFailed {
					// Drain all already-recorded delta pages before emitting terminal state.
					if page.NextAfter != nil {
						continue
					}
					e := agentruntime.Event{Kind: kind, RunID: id, Status: r.Status, Text: r.ResultText}
					if r.Error != "" {
						e.Err = errors.New(r.Error)
					}
					if !send(e) {
						return
					}
					lastStatus = r.Status
					if e.IsTerminal() {
						return
					}
				}
			}
			delay := q.poll
			if failures > 0 {
				delay = time.Duration(min(failures, 10)) * q.poll
			}
			timer := time.NewTimer(delay)
			select {
			case <-child.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return s, nil
}

var _ agentruntime.QueueManager = (*Queue)(nil)
