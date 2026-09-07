package agentruntime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// Observe the storage contract, not runtime's private completion state.
type lifecycleStore struct {
	conversation.Store
	commits  atomic.Int32
	closes   atomic.Int32
	released chan struct{}
	commit   func(context.Context) error
}

type lifecycleLease struct {
	conversation.Lease
	owner *lifecycleStore
}

func (s *lifecycleStore) Begin(ctx context.Context, user, id string) (conversation.Lease, error) {
	lease, err := s.Store.Begin(ctx, user, id)
	if err != nil {
		return nil, err
	}
	return &lifecycleLease{Lease: lease, owner: s}, nil
}

func (l *lifecycleLease) Commit(ctx context.Context, messages []*schema.AgenticMessage, turn conversation.Turn) error {
	l.owner.commits.Add(1)
	if l.owner.commit != nil {
		if err := l.owner.commit(ctx); err != nil {
			return err
		}
	}
	return l.Lease.Commit(ctx, messages, turn)
}

func (l *lifecycleLease) Close() {
	l.Lease.Close()
	l.owner.closes.Add(1)
	l.owner.released <- struct{}{}
}

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not finish")
		var zero T
		return zero
	}
}

func closeRun(t *testing.T, run Run) Event {
	t.Helper()
	result := make(chan Event, 1)
	go func() { result <- run.Close() }()
	return await(t, result)
}

func TestCancellationFinalizesWithoutFurtherReads(t *testing.T) {
	for _, parentCanceled := range []bool{false, true} {
		name := map[bool]string{false: "Cancel", true: "parent_context"}[parentCanceled]
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			memory := conversation.NewMemory()
			s, err := memory.Create(parent, "alice")
			if err != nil {
				t.Fatal(err)
			}
			store := &lifecycleStore{Store: memory, released: make(chan struct{}, 10)}
			session := NewStored(parent, streamAgent([]*schema.AgenticMessage{testutil.Text("partial")}), store, "alice", s.ID)
			run, err := session.Start("hi")
			if err != nil {
				t.Fatal(err)
			}
			if event := run.Next(); event.Kind != EventTextDelta {
				t.Fatalf("first event: %+v", event)
			}
			if parentCanceled {
				cancel()
			} else {
				run.Cancel()
			}
			// Neither Next nor Close drives finalization here.
			await(t, store.released)
			ctx := context.Background()
			lease, err := memory.Begin(ctx, "alice", s.ID)
			if err != nil {
				t.Fatalf("lease remained locked: %v", err)
			}
			if messages := lease.Messages(); len(messages) != 1 || messages[0].Role != schema.AgenticRoleTypeUser {
				t.Fatal("partial output entered the next context")
			}
			lease.Close()
			turns, err := memory.History(ctx, "alice", s.ID, 0, 10)
			if err != nil || len(turns) != 1 || turns[0].Status != "canceled" || len(turns[0].Messages) != 2 {
				t.Fatalf("audit=%+v err=%v", turns, err)
			}
			if turns[0].Messages[1].ContentBlocks[0].AssistantGenText.Text != "partial" {
				t.Fatal("received native output was lost")
			}
			results := make(chan Event, 8)
			for i := 0; i < cap(results); i++ {
				go func() { results <- run.Close() }()
			}
			for i := 0; i < cap(results); i++ {
				if event := await(t, results); event.Kind != EventCanceled || event.Text != "partial" || event.Err != nil {
					t.Fatalf("Close: %+v", event)
				}
			}
			if event := run.Next(); event.Kind != EventCanceled {
				t.Fatalf("Close consumed the terminal event: %+v", event)
			}
			if event := run.Next(); !errors.Is(event.Err, ErrRunFinished) {
				t.Fatalf("terminal event repeated: %+v", event)
			}
			if store.commits.Load() != 1 || store.closes.Load() != 1 {
				t.Fatalf("commits=%d closes=%d", store.commits.Load(), store.closes.Load())
			}
			// An uncanceled request context also verifies the local reservation.
			next, err := session.StartContext(ctx, "again")
			if err != nil {
				t.Fatalf("local reservation remained held: %v", err)
			}
			closeRun(t, next)
		})
	}
}

func TestCloseBeforeFirstReadReleasesReservation(t *testing.T) {
	ctx := context.Background()
	memory := conversation.NewMemory()
	s, _ := memory.Create(ctx, "alice")
	var calls atomic.Int32
	agent := &scriptedAgent{run: func(context.Context, *adk.TypedAgentInput[*schema.AgenticMessage]) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
		calls.Add(1)
		return nil
	}}
	session := NewStored(ctx, agent, memory, "alice", s.ID)
	for i := 0; i < 2; i++ {
		run, err := session.Start("unused")
		if err != nil {
			t.Fatal(err)
		}
		if event := closeRun(t, run); event.Kind != EventCanceled {
			t.Fatalf("Close: %+v", event)
		}
	}
	turns, err := memory.History(ctx, "alice", s.ID, 0, 10)
	if err != nil || len(turns) != 0 || calls.Load() != 0 {
		t.Fatalf("unstarted run performed I/O: turns=%v calls=%d err=%v", turns, calls.Load(), err)
	}
}

type loadingStore struct {
	conversation.Store
	entered chan struct{}
}

func (s loadingStore) Begin(ctx context.Context, _, _ string) (conversation.Lease, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCloseDuringHistoryLoad(t *testing.T) {
	ctx := context.Background()
	memory := conversation.NewMemory()
	s, _ := memory.Create(ctx, "alice")
	entered := make(chan struct{})
	session := NewStored(ctx, streamAgent(nil), loadingStore{memory, entered}, "alice", s.ID)
	run, _ := session.Start("hi")
	next := make(chan Event, 1)
	go func() { next <- run.Next() }()
	await(t, entered)
	if event := closeRun(t, run); event.Kind != EventCanceled {
		t.Fatalf("Close: %+v", event)
	}
	if event := await(t, next); event.Kind != EventCanceled {
		t.Fatalf("Next: %+v", event)
	}
	again, err := session.Start("again")
	if err != nil {
		t.Fatalf("loading cancellation kept local reservation: %v", err)
	}
	closeRun(t, again)
}

func TestCloseWhileNextWaitsForAgent(t *testing.T) {
	ctx := context.Background()
	memory := conversation.NewMemory()
	s, _ := memory.Create(ctx, "alice")
	entered := make(chan struct{})
	agent := &scriptedAgent{run: func(ctx context.Context, _ *adk.TypedAgentInput[*schema.AgenticMessage]) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
		iter, gen := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
		go func() {
			close(entered)
			<-ctx.Done()
			gen.Close()
		}()
		return iter
	}}
	run, _ := NewStored(ctx, agent, memory, "alice", s.ID).Start("hi")
	next := make(chan Event, 1)
	go func() { next <- run.Next() }()
	await(t, entered)
	if event := closeRun(t, run); event.Kind != EventCanceled {
		t.Fatalf("Close: %+v", event)
	}
	if event := await(t, next); event.Kind != EventCanceled {
		t.Fatalf("iterator EOF after cancellation became success: %+v", event)
	}
	lease, err := memory.Begin(ctx, "alice", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
}

func TestCloseWaitsForCommitAndPreservesFailure(t *testing.T) {
	ctx := context.Background()
	memory := conversation.NewMemory()
	s, _ := memory.Create(ctx, "alice")
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	failure := errors.New("disk unavailable")
	store := &lifecycleStore{Store: memory, released: make(chan struct{}, 10), commit: func(ctx context.Context) error {
		entered <- ctx
		<-release
		return failure
	}}
	run, _ := NewStored(ctx, streamAgent([]*schema.AgenticMessage{testutil.Text("partial")}), store, "alice", s.ID).Start("hi")
	run.Next()
	result := make(chan Event, 1)
	go func() { result <- run.Close() }()
	cleanup := await(t, entered)
	if cleanup.Err() != nil {
		t.Fatal("commit inherited canceled context")
	}
	if deadline, ok := cleanup.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
		t.Fatal("commit cleanup has no bounded deadline")
	}
	select {
	case event := <-result:
		t.Fatalf("Close returned before commit: %+v", event)
	default:
	}
	if lease, err := memory.Begin(ctx, "alice", s.ID); !errors.Is(err, conversation.ErrBusy) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("lease released before commit: %v", err)
	}
	release <- struct{}{}
	for _, event := range []Event{await(t, result), closeRun(t, run), run.Next()} {
		if event.Kind != EventFailed || !errors.Is(event.Err, failure) {
			t.Fatalf("commit error was lost: %+v", event)
		}
	}
	lease, err := memory.Begin(ctx, "alice", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	turns, _ := memory.History(ctx, "alice", s.ID, 0, 10)
	if len(turns) != 0 || store.commits.Load() != 1 || store.closes.Load() != 1 {
		t.Fatal("failed commit persisted a turn or repeated cleanup")
	}
}

func TestClosePreservesCompletionAndPendingToolEvents(t *testing.T) {
	ctx := context.Background()
	memory := conversation.NewMemory()
	s, _ := memory.Create(ctx, "alice")
	store := &lifecycleStore{Store: memory, released: make(chan struct{}, 10)}
	// A tool call without CallID is flushed only when the stream is closed.
	message := testutil.ToolCall("", `{"a":2}`)
	run, _ := NewStored(ctx, streamAgent([]*schema.AgenticMessage{message}), store, "alice", s.ID).Start("hi")
	if event := run.Next(); event.Kind != EventToolStarted || event.Arguments != `{"a":2}` {
		t.Fatalf("tool event lost: %+v", event)
	}
	if event := run.Next(); event.Kind != EventCompleted {
		t.Fatalf("terminal: %+v", event)
	}
	if event := closeRun(t, run); event.Kind != EventCompleted || event.Err != nil {
		t.Fatalf("Close changed completed outcome: %+v", event)
	}
	if store.commits.Load() != 1 || store.closes.Load() != 1 {
		t.Fatal("Close repeated successful persistence")
	}
}

func TestCloseDoesNotConsumePendingEvents(t *testing.T) {
	ctx := context.Background()
	memory := conversation.NewMemory()
	s, _ := memory.Create(ctx, "alice")
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{
		schema.NewContentBlock(&schema.Reasoning{Text: "think"}),
		schema.NewContentBlock(&schema.AssistantGenText{Text: "partial"}),
		schema.NewContentBlock(&schema.FunctionToolCall{Name: "calculator", CallID: "c1", Arguments: `{"a":2}`}),
	}}
	run, _ := NewStored(ctx, streamAgent([]*schema.AgenticMessage{message}), memory, "alice", s.ID).Start("hi")
	if event := run.Next(); event.Kind != EventReasoningDelta {
		t.Fatalf("first event=%+v", event)
	}
	if event := closeRun(t, run); event.Kind != EventCanceled || event.Text != "partial" {
		t.Fatalf("Close=%+v", event)
	}
	for _, kind := range []EventKind{EventTextDelta, EventToolStarted, EventCanceled} {
		if event := run.Next(); event.Kind != kind {
			t.Fatalf("Next=%+v want %s", event, kind)
		}
	}
}
