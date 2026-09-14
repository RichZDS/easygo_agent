package agentruntime

import (
	"context"
	"testing"
	"time"

	"easygo-agent/internal/conversation"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type identityCaptureAgent struct {
	identities chan InvocationIdentity
}

func (agent *identityCaptureAgent) Name(context.Context) string        { return "identity-capture" }
func (agent *identityCaptureAgent) Description(context.Context) string { return "test" }
func (agent *identityCaptureAgent) Run(ctx context.Context, _ *adk.TypedAgentInput[*schema.AgenticMessage], _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
	identity, _ := InvocationIdentityFromContext(ctx)
	agent.identities <- identity
	iterator, generator := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
	generator.Close()
	return iterator
}

func TestInvocationIdentityContext(t *testing.T) {
	ctx := WithInvocationIdentity(context.Background(), InvocationIdentity{SessionID: " session-1 ", RunID: " run-1 "})
	identity, ok := InvocationIdentityFromContext(ctx)
	if !ok || identity.SessionID != "session-1" || identity.RunID != "run-1" {
		t.Fatalf("identity=%+v ok=%v", identity, ok)
	}
	for _, ctx := range []context.Context{
		nil,
		context.Background(),
		WithInvocationIdentity(context.Background(), InvocationIdentity{SessionID: "session-1"}),
	} {
		if identity, ok := InvocationIdentityFromContext(ctx); ok {
			t.Fatalf("unexpected identity: %+v", identity)
		}
	}
}

func TestClaimedRunCarriesDurableInvocationIdentity(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	agent := &identityCaptureAgent{identities: make(chan InvocationIdentity, 1)}
	run, err := ClaimQueuedRun(ctx, store, agent, "alice", session.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if event := run.Next(); event.Kind != EventCompleted {
		t.Fatalf("terminal=%+v", event)
	}
	identity := <-agent.identities
	if identity.SessionID != session.ID {
		t.Fatalf("identity=%+v", identity)
	}
	if _, err := uuid.Parse(identity.RunID); err != nil {
		t.Fatalf("run ID %q is not a UUID: %v", identity.RunID, err)
	}
	run.Close()
}

func TestQueuedRunCarriesDurableInvocationIdentity(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Enqueue(ctx, "alice", session.ID, "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimNext(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	agent := &identityCaptureAgent{identities: make(chan InvocationIdentity, 1)}
	run := NewClaimed(ctx, agent, lease)
	if event := run.Next(); event.Kind != EventCompleted {
		t.Fatalf("terminal=%+v", event)
	}
	identity := <-agent.identities
	if identity.SessionID != record.SessionID || identity.RunID != record.ID {
		t.Fatalf("identity=%+v record=%+v", identity, record)
	}
	run.Close()
}
