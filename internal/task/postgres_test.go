package task

import (
	"context"
	"errors"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/internal/conversation"
	"easygo-agent/internal/toolregistry"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ownerStore struct {
	Store
	owner Owner
}

func (s ownerStore) Create(ctx context.Context, _ Owner, b Brief) (Task, error) {
	return s.Store.Create(ctx, s.owner, b)
}
func postgresFixture(t *testing.T) (*Postgres, *conversation.Postgres, conversation.RunLease, Owner, string) {
	t.Helper()
	dsn := os.Getenv("EASYGO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set EASYGO_TEST_DATABASE_URL for PostgreSQL fault injection")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "task_test_" + uuid.New().String()[:8]
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	scoped := u.String()
	conversations, err := conversation.NewPostgres(ctx, scoped, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conversations.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+name+" CASCADE")
		admin.Close()
	})
	store, err := NewPostgres(ctx, conversations.Pool())
	if err != nil {
		t.Fatal(err)
	}
	session, err := conversations.Create(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	run, err := conversations.Enqueue(ctx, "owner", session.ID, "delegate", "parent")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := conversations.ClaimNext(ctx, "parent-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Close)
	return store, conversations, lease, Owner{"owner", session.ID, run.ID}, scoped
}
func TestPostgresRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		after      bool
		retry      toolregistry.Retry
		want       Status
		calls      int32
	}{
		{"model-checkpoint", "model_output", true, toolregistry.Unsafe, Completed, 1},
		{"unknown-write-outcome", "tool_result", false, toolregistry.Unsafe, Blocked, 1},
		{"saved-write-outcome", "tool_result", true, toolregistry.Unsafe, Completed, 1},
		{"readonly-retry", "tool_result", false, toolregistry.ReadOnly, Completed, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, conversations, _, owner, dsn := postgresFixture(t)
			fault := &crashStore{Store: ownerStore{store, owner}, after: tc.after, match: func(task Task) bool { return len(task.Events) > 0 && task.Events[len(task.Events)-1].Kind == tc.kind }}
			e, claimed, count := fixture(t, fault, tc.retry)
			if err := e.Execute(ctx, claimed); !errors.Is(err, crash) {
				t.Fatal(err)
			}
			// Expire without orderly release, then use a new pool as the restarted process.
			if _, err := conversations.Pool().Exec(ctx, "UPDATE agent_tasks SET lease_expires_at=now()-interval '1 second' WHERE id=$1", claimed.ID); err != nil {
				t.Fatal(err)
			}
			restarted, err := conversation.NewPostgres(ctx, dsn, 8)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			second, err := NewPostgres(ctx, restarted.Pool())
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := second.Claim(ctx, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Save(ctx, claimed, claimed.Token); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("stale process committed: %v", err)
			}
			e.Store = second
			if err = e.Execute(ctx, recovered); err != nil {
				t.Fatal(err)
			}
			got, err := second.Get(ctx, owner, claimed.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want || count.Load() != tc.calls {
				t.Fatalf("status %s calls %d", got.Status, count.Load())
			}
			var events, calls int
			if err = conversations.Pool().QueryRow(ctx, "SELECT count(*) FROM agent_task_events WHERE task_id=$1", got.ID).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if events != len(got.Events) {
				t.Fatal("event log incomplete")
			}
			if err = conversations.Pool().QueryRow(ctx, "SELECT count(*) FROM agent_task_calls WHERE task_id=$1", got.ID).Scan(&calls); err != nil {
				t.Fatal(err)
			}
			if calls < 1 {
				t.Fatal("tool call log absent")
			}
		})
	}
}
func TestPostgresClaimCompetitionAndIsolation(t *testing.T) {
	ctx := context.Background()
	s, _, _, o, _ := postgresFixture(t)
	created, err := s.Create(ctx, o, Brief{Role: "executor", Goal: "x", Acceptance: []string{"done"}, Key: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, Owner{User: "intruder", Session: o.Session}, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-user read")
	}
	var n atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Claim(ctx, time.Minute); err == nil {
				n.Add(1)
			} else if !errors.Is(err, ErrNoTask) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("claims=%d", n.Load())
	}
	if _, err = s.Resume(ctx, o, created.ID, Resume{}); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent resume")
	}
}
func TestPostgresNotificationCrashAndAtomicAcknowledgement(t *testing.T) {
	ctx := context.Background()
	s, conversations, parent, o, _ := postgresFixture(t)
	e, c, _ := fixture(t, ownerStore{s, o}, toolregistry.ReadOnly)
	if err := e.Execute(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := parent.CommitRun(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("original brief")}, conversation.RunCompleted, nil, "submitted", ""); err != nil {
		t.Fatal(err)
	}
	parent.Close()
	userRun, err := conversations.Enqueue(ctx, o.User, o.Session, "new constraint: concise", "user-next")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("outbox %+v %v", pending, err)
	}
	n := pending[0]
	first, err := conversations.EnqueueInternal(ctx, o.User, o.Session, n.Content, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Crash after enqueue and before marking the outbox. Dispatch must reuse it.
	service := &Service{Store: s, Conversations: conversations}
	if err = service.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	retry, err := conversations.EnqueueInternal(ctx, o.User, o.Session, n.Content, n.ID)
	if err != nil || retry.ID != first.ID {
		t.Fatalf("duplicate notification %v", err)
	}
	next, err := conversations.ClaimNext(ctx, "user-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.Run().ID != userRun.ID {
		t.Fatal("notification jumped existing user input")
	}
	if err = next.CommitRun(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage(userRun.Input)}, conversation.RunCompleted, nil, "ack", ""); err != nil {
		t.Fatal(err)
	}
	next.Close()
	summary, err := conversations.ClaimNext(ctx, "summary", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer summary.Close()
	if summary.Run().Source != "task_notification" || summary.Messages()[0].ContentBlocks[0].UserInputText.Text != userRun.Input {
		t.Fatal("notification lost source/latest context")
	}
	if conversation.RunInput(summary.Run()).Role != schema.AgenticRoleTypeSystem {
		t.Fatal("notification impersonated user")
	}
	var ack bool
	if err = conversations.Pool().QueryRow(ctx, "SELECT acknowledged FROM agent_task_notifications WHERE id=$1", n.ID).Scan(&ack); err != nil || ack {
		t.Fatal("premature ack")
	}
	if err = summary.CommitRun(ctx, summary.Messages(), conversation.RunCompleted, []*schema.AgenticMessage{schema.SystemAgenticMessage("summary")}, "summary", ""); err != nil {
		t.Fatal(err)
	}
	if err = conversations.Pool().QueryRow(ctx, "SELECT acknowledged FROM agent_task_notifications WHERE id=$1", n.ID).Scan(&ack); err != nil || !ack {
		t.Fatal("summary not atomically acknowledged")
	}
	if _, err = conversations.ClaimNext(ctx, "duplicate", time.Minute); !errors.Is(err, conversation.ErrNoQueuedRun) {
		t.Fatal("notification duplicated")
	}
}
