package task

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"
	"easygo-agent/internal/toolregistry"
	"github.com/cloudwego/eino/schema"
)

func TestWorkersIndependentBoundedAndShutdownRecoverable(t *testing.T) {
	ctx := context.Background()
	conversations := conversation.NewMemory()
	session, _ := conversations.Create(ctx, "owner")
	run, _ := conversations.Enqueue(ctx, "owner", session.ID, "delegate", "parent")
	parent, err := conversations.ClaimNext(ctx, "parent", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	store := NewMemory()
	cfg := DefaultConfig()
	cfg.LeaseTTL = 90 * time.Millisecond
	cfg.PollInterval = time.Millisecond
	cfg.Roles = map[string]Role{"analyst": {}}
	var active, peak atomic.Int32
	gate := make(chan struct{})
	entered := make(chan struct{}, 10)
	engine := &Engine{Store: store, Registry: toolregistry.New(), Config: cfg, Model: &testutil.Model{GenerateFunc: func(ctx context.Context, in []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-gate:
			return callMessage("done", "finish_task", `{"summary":"done","evidence":["checked"]}`), nil
		}
	}}}
	service := &Service{Store: store, Engine: engine, Conversations: conversations}
	o := Owner{"owner", session.ID, run.ID}
	for _, key := range []string{"one", "two", "three"} {
		if _, err = service.Spawn(ctx, o, Brief{Role: "analyst", Goal: key, Acceptance: []string{"checked"}, Key: key}); err != nil {
			t.Fatal(err)
		}
	}
	if err = service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("worker never started")
		}
	}
	if peak.Load() != 2 {
		t.Fatalf("peak %d", peak.Load())
	}
	// Parent can complete and the next user turn can claim the session while workers wait.
	if err = parent.CommitRun(ctx, nil, conversation.RunCompleted, nil, "submitted", ""); err != nil {
		t.Fatal(err)
	}
	parent.Close()
	next, _ := conversations.Enqueue(ctx, "owner", session.ID, "latest constraints", "next")
	lease, err := conversations.ClaimNext(ctx, "main", time.Minute)
	if err != nil || lease.Run().ID != next.ID {
		t.Fatalf("child held session lock: %v", err)
	}
	_ = lease.CommitRun(ctx, nil, conversation.RunCompleted, nil, "ok", "")
	lease.Close()
	service.Close()
	tasks, _ := store.List(ctx, o)
	for _, item := range tasks {
		if item.Status != Queued {
			t.Fatalf("shutdown canceled %s", item.Status)
		}
	}
	close(gate)
	restarted := &Service{Store: store, Engine: engine, Conversations: conversations}
	if err = restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	waitTasks(t, store, o, func(tasks []Task) bool {
		for _, task := range tasks {
			if task.Status != Completed {
				return false
			}
		}
		return true
	})
	if peak.Load() > 2 {
		t.Fatalf("worker limit exceeded: %d", peak.Load())
	}
}
func TestCancelParentChildrenAndInternalDelegationDenied(t *testing.T) {
	ctx := context.Background()
	conversations := conversation.NewMemory()
	session, _ := conversations.Create(ctx, "u")
	run, _ := conversations.Enqueue(ctx, "u", session.ID, "work", "")
	parent, _ := conversations.ClaimNext(ctx, "main", time.Minute)
	defer parent.Close()
	store := NewMemory()
	engine := &Engine{Config: DefaultConfig()}
	service := &Service{Store: store, Engine: engine, Conversations: conversations}
	o := Owner{"u", session.ID, run.ID}
	child, err := service.Spawn(ctx, o, Brief{Role: "analyst", Goal: "x", Acceptance: []string{"x"}, Key: "key"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conversations.RequestCancel(ctx, o.User, o.Session, o.Run)
	if err = service.CancelChildren(ctx, o.User, o.Session, o.Run); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(ctx, o, child.ID)
	if got.Status != Canceled {
		t.Fatal(got.Status)
	}
	_ = parent.CommitRun(ctx, nil, conversation.RunCanceled, nil, "", "")
	parent.Close()
	notification, _ := conversations.EnqueueInternal(ctx, "u", session.ID, "evidence", "n1")
	internal, _ := conversations.ClaimNext(ctx, "notification", time.Minute)
	defer internal.Close()
	if _, err = service.Spawn(ctx, Owner{"u", session.ID, notification.ID}, Brief{Role: "analyst", Goal: "recursive", Acceptance: []string{"x"}, Key: "no"}); err == nil {
		t.Fatal("notification delegated")
	}
}
func waitTasks(t *testing.T, s Store, o Owner, ready func([]Task) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := s.List(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if ready(tasks) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("tasks did not reach expected state")
}
