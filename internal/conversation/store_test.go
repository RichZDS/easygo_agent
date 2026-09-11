package conversation

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"os"
	"testing"
	"time"
)

func TestMemoryContract(t *testing.T) { testStore(t, NewMemory()) }

// Set EASYGO_TEST_DATABASE_URL to a disposable PostgreSQL database. Tables are
// additive; the test only cleans up its uniquely named user's records.
func TestPostgresContract(t *testing.T) {
	dsn := os.Getenv("EASYGO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set EASYGO_TEST_DATABASE_URL for PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := NewPostgres(ctx, dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	testStore(t, store)
	testQueueStore(t, store)
}

func testQueueStore(t *testing.T, store QueueStore) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	user := "queue-test-" + uuid.NewString()
	session, err := store.Create(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if pg, ok := store.(*Postgres); ok {
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_, _ = pg.pool.Exec(cleanup, "DELETE FROM agent_runs WHERE session_id IN (SELECT id FROM agent_sessions WHERE username=$1)", user)
			_, _ = pg.pool.Exec(cleanup, "DELETE FROM agent_turns WHERE session_id IN (SELECT id FROM agent_sessions WHERE username=$1)", user)
			_, _ = pg.pool.Exec(cleanup, "DELETE FROM agent_sessions WHERE username=$1", user)
		})
	}
	run, err := store.Enqueue(ctx, user, session.ID, "queued input", "idempotent")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.Enqueue(ctx, user, session.ID, "queued input", "idempotent")
	if err != nil || retry.ID != run.ID {
		t.Fatalf("idempotent retry: %+v %v", retry, err)
	}
	lease, err := store.ClaimNext(ctx, "contract-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Run().ID != run.ID || lease.Run().Status != RunRunning {
		t.Fatalf("claim: %+v", lease.Run())
	}
	if err = lease.CommitRun(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("context")}, RunCompleted, []*schema.AgenticMessage{schema.UserAgenticMessage("output")}, "output", ""); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	completed, err := store.GetRun(ctx, user, session.ID, run.ID)
	if err != nil || completed.Status != RunCompleted || completed.TurnID == 0 {
		t.Fatalf("completed: %+v %v", completed, err)
	}
}

func testStore(t *testing.T, store Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	user := "test-" + uuid.NewString()
	session, err := store.Create(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if pg, ok := store.(*Postgres); ok {
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_, _ = pg.pool.Exec(cleanup, "DELETE FROM agent_runs WHERE session_id IN (SELECT id FROM agent_sessions WHERE username=$1)", user)
			_, _ = pg.pool.Exec(cleanup, "DELETE FROM agent_turns WHERE session_id IN (SELECT id FROM agent_sessions WHERE username=$1)", user)
			_, _ = pg.pool.Exec(cleanup, "DELETE FROM agent_sessions WHERE username=$1", user)
		}()
	}
	if _, err = store.Begin(ctx, "other", session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user access: %v", err)
	}
	lease, err := store.Begin(ctx, user, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err = store.Begin(ctx, user, session.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("second writer: %v", err)
	}
	input := schema.UserAgenticMessage("原始输入")
	output := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{CallID: "abc", Name: "calculator", Arguments: `{"a":2}`})}, ResponseMeta: &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{TotalTokens: 42}}}
	if err = CommitRun(ctx, lease, []*schema.AgenticMessage{schema.UserAgenticMessage("压缩摘要")}, "completed", "原始输入", []*schema.AgenticMessage{output}); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	input.ContentBlocks[0].UserInputText.Text = "mutated caller"
	lease, err = store.Begin(ctx, user, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.Messages()) != 1 || lease.Messages()[0].ContentBlocks[0].UserInputText.Text != "压缩摘要" {
		t.Fatal("context was not restored")
	}
	lease.Close()
	turns, err := store.History(ctx, user, session.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Messages[0].ContentBlocks[0].UserInputText.Text != "原始输入" {
		t.Fatal("raw transcript lost or aliased")
	}
	if turns[0].Messages[1].ResponseMeta.TokenUsage.TotalTokens != 42 || turns[0].Messages[1].ContentBlocks[0].FunctionToolCall.CallID != "abc" {
		t.Fatal("native fields lost")
	}
	tail, err := store.History(ctx, user, session.ID, turns[0].ID, 1)
	if err != nil || len(tail) != 0 {
		t.Fatalf("cursor pagination: %v %v", tail, err)
	}
	sessions, err := store.List(ctx, user, 1, 0)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("list: %v %v", sessions, err)
	}
	if _, err = store.History(ctx, "other", session.ID, 0, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("history ownership: %v", err)
	}
	if _, err = store.Begin(ctx, user, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown ID: %v", err)
	}
}
