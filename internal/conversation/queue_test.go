package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestMemoryQueueLifecycle(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.Enqueue(ctx, "queue-user", session.ID, "first", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Enqueue(ctx, "queue-user", session.ID, "second", "key-2")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != RunQueued || first.Position != 1 || second.Position != 2 {
		t.Fatalf("unexpected queue positions: first=%+v second=%+v", first, second)
	}

	retried, err := store.Enqueue(ctx, "queue-user", session.ID, "first", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID != first.ID {
		t.Fatalf("idempotent enqueue created a new run: %q != %q", retried.ID, first.ID)
	}
	if _, err = store.Enqueue(ctx, "queue-user", session.ID, "different", "key-1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}

	lease, err := store.ClaimNext(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if got := lease.Run(); got.ID != first.ID || got.Status != RunRunning {
		t.Fatalf("unexpected claim: %+v", got)
	}
	if got, err := store.GetRun(ctx, "queue-user", session.ID, second.ID); err != nil || got.Position != 1 {
		t.Fatalf("second position after claim = %+v, err=%v", got, err)
	}

	if _, err = store.RequestCancel(ctx, "queue-user", session.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	canceled, err := store.GetRun(ctx, "queue-user", session.ID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != RunCanceled {
		t.Fatalf("queued cancel did not finish: %+v", canceled)
	}

	if err = lease.CommitRun(ctx,
		[]*schema.AgenticMessage{schema.UserAgenticMessage("context")},
		RunCompleted,
		[]*schema.AgenticMessage{schema.UserAgenticMessage("done")},
		"done", ""); err != nil {
		t.Fatal(err)
	}
	completed, err := store.GetRun(ctx, "queue-user", session.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != RunCompleted || completed.ResultText != "done" || completed.TurnID == 0 {
		t.Fatalf("unexpected completed run: %+v", completed)
	}
	history, err := store.History(ctx, "queue-user", session.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || len(history[0].Messages) != 2 {
		t.Fatalf("audit history was not committed: %+v", history)
	}
}

func TestMemoryQueueRunningCancellationAndExpiry(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Enqueue(ctx, "queue-user", session.ID, "cancel me", "")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimNext(ctx, "worker-1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RequestCancel(ctx, "queue-user", session.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	cancelRequested, err := lease.CancelRequested(ctx)
	if err != nil || !cancelRequested {
		t.Fatalf("cancel request was not visible to lease: %v, %v", cancelRequested, err)
	}
	if err = lease.CommitRun(ctx, nil, RunCanceled, nil, "", "canceled"); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	got, err := store.GetRun(ctx, "queue-user", session.ID, run.ID)
	if err != nil || got.Status != RunCanceled {
		t.Fatalf("unexpected canceled run: %+v, %v", got, err)
	}

	expiring, err := store.Enqueue(ctx, "queue-user", session.ID, "recover me", "")
	if err != nil {
		t.Fatal(err)
	}
	oldLease, err := store.ClaimNext(ctx, "worker-old", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	claimed := oldLease.Run()
	if err = store.RecoverExpired(ctx, claimed.LeaseExpiresAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = oldLease.Heartbeat(ctx, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale heartbeat error = %v", err)
	}
	oldLease.Close()

	newLease, err := store.ClaimNext(ctx, "worker-new", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer newLease.Close()
	if newLease.Run().ID != expiring.ID {
		t.Fatalf("recovered run was not claimable: %+v", newLease.Run())
	}
}

func TestMemoryQueueCancellationWinsFinalCommitRace(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Enqueue(ctx, "queue-user", session.ID, "race", "")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimNext(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RequestCancel(ctx, "queue-user", session.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if err = lease.CommitRun(ctx, nil, RunCompleted, nil, "done", ""); !errors.Is(err, ErrCancellationRequested) {
		t.Fatalf("completion unexpectedly won cancellation race: %v", err)
	}
	if err = lease.CommitRun(ctx, nil, RunCanceled, nil, "", ""); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	got, err := store.GetRun(ctx, "queue-user", session.ID, run.ID)
	if err != nil || got.Status != RunCanceled {
		t.Fatalf("run status=%+v err=%v", got, err)
	}
}

func TestMemoryQueueClaimsDifferentSessions(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	firstSession, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	secondSession, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Enqueue(ctx, "queue-user", firstSession.ID, "one", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Enqueue(ctx, "queue-user", secondSession.ID, "two", "")
	if err != nil {
		t.Fatal(err)
	}
	lease1, err := store.ClaimNext(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease1.Close()
	lease2, err := store.ClaimNext(ctx, "worker-2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease2.Close()
	if lease1.Run().ID == lease2.Run().ID ||
		(lease1.Run().ID != first.ID && lease1.Run().ID != second.ID) ||
		(lease2.Run().ID != first.ID && lease2.Run().ID != second.ID) {
		t.Fatalf("unexpected claims: %q, %q", lease1.Run().ID, lease2.Run().ID)
	}
}

func TestMemoryQueueLimitAndPositions(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryWithQueueLimit(2)
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Enqueue(ctx, "queue-user", session.ID, "one", ""); err != nil {
		t.Fatal(err)
	}
	second, err := store.Enqueue(ctx, "queue-user", session.ID, "two", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Enqueue(ctx, "queue-user", session.ID, "three", ""); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected queue full, got %v", err)
	}
	if _, err = store.RequestCancel(ctx, "queue-user", session.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Enqueue(ctx, "queue-user", session.ID, "three", ""); err != nil {
		t.Fatal(err)
	}
}
