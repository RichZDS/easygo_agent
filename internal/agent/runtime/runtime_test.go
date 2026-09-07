package agentruntime

import (
	"context"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"
	"errors"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

type scriptedAgent struct {
	run func(context.Context, *adk.TypedAgentInput[*schema.AgenticMessage]) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]]
}

func (s *scriptedAgent) Name(context.Context) string        { return "scripted" }
func (s *scriptedAgent) Description(context.Context) string { return "test" }
func (s *scriptedAgent) Run(ctx context.Context, input *adk.TypedAgentInput[*schema.AgenticMessage], _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
	return s.run(ctx, input)
}
func streamAgent(chunks []*schema.AgenticMessage) *scriptedAgent {
	return &scriptedAgent{run: func(context.Context, *adk.TypedAgentInput[*schema.AgenticMessage]) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
		iter, gen := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
		go func() {
			defer gen.Close()
			gen.Send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Output: &adk.TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: &adk.TypedMessageVariant[*schema.AgenticMessage]{IsStreaming: true, MessageStream: schema.StreamReaderFromArray(chunks)}}})
		}()
		return iter
	}}
}
func drain(run Run) Event {
	for {
		e := run.Next()
		if e.Kind == EventCompleted || e.Kind == EventCanceled || e.Kind == EventFailed {
			return e
		}
	}
}

func reasoningChunk(index int, text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{
		schema.NewContentBlockChunk(&schema.Reasoning{Text: text}, &schema.StreamingMeta{Index: index}),
	}}
}

func assistantTextChunk(index int, text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{
		schema.NewContentBlockChunk(&schema.AssistantGenText{Text: text}, &schema.StreamingMeta{Index: index}),
	}}
}

func TestNativeStreamingChunksAreConcatenated(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	s, _ := store.Create(ctx, "alice")
	agent := streamAgent([]*schema.AgenticMessage{reasoningChunk(0, "think "), reasoningChunk(0, "done"), assistantTextChunk(1, "hel"), assistantTextChunk(1, "lo")})
	run, _ := NewStored(ctx, agent, store, "alice", s.ID).Start("hi")
	last := drain(run)
	if last.Kind != EventCompleted || last.Text != "hello" {
		t.Fatalf("terminal: %+v", last)
	}
	turns, _ := store.History(ctx, "alice", s.ID, 0, 100)
	output := turns[0].Messages[1]
	if len(output.ContentBlocks) != 2 || output.ContentBlocks[0].Reasoning.Text != "think done" || output.ContentBlocks[1].AssistantGenText.Text != "hello" {
		t.Fatalf("native output: %s", output)
	}
}
func TestCancelDuringStreamReleasesSessionWithoutPartialContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := conversation.NewMemory()
	s, _ := store.Create(ctx, "alice")
	agent := &scriptedAgent{run: func(ctx context.Context, _ *adk.TypedAgentInput[*schema.AgenticMessage]) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
		iter, gen := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
		reader, writer := schema.Pipe[*schema.AgenticMessage](1)
		go func() {
			defer gen.Close()
			gen.Send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Output: &adk.TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: &adk.TypedMessageVariant[*schema.AgenticMessage]{IsStreaming: true, MessageStream: reader}}})
			writer.Send(testutil.Text("partial"), nil)
			<-ctx.Done()
			writer.Close()
		}()
		return iter
	}}
	session := NewStored(ctx, agent, store, "alice", s.ID)
	run, _ := session.Start("hi")
	if e := run.Next(); e.Kind != EventTextDelta {
		t.Fatalf("first=%+v", e)
	}
	if _, err := session.Start("concurrent"); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("concurrent: %v", err)
	}
	run.Cancel()
	if last := drain(run); last.Kind != EventCanceled {
		t.Fatalf("terminal=%+v", last)
	}
	lease, err := store.Begin(ctx, "alice", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if len(lease.Messages()) != 1 {
		t.Fatal("partial output entered context")
	}
	turns, _ := store.History(ctx, "alice", s.ID, 0, 10)
	if len(turns) != 1 || turns[0].Status != "canceled" {
		t.Fatal("cancel was not audited")
	}
	if len(turns[0].Messages) != 2 || turns[0].Messages[1].ContentBlocks[0].AssistantGenText.Text != "partial" {
		t.Fatal("received partial output was not preserved in audit")
	}
}

type failingStore struct{ conversation.Store }
type failingLease struct{ conversation.Lease }

func (s failingStore) Begin(ctx context.Context, user, id string) (conversation.Lease, error) {
	l, err := s.Store.Begin(ctx, user, id)
	if err != nil {
		return nil, err
	}
	return failingLease{l}, nil
}
func (l failingLease) Commit(context.Context, []*schema.AgenticMessage, conversation.Turn) error {
	return errors.New("disk unavailable")
}
func TestCommitFailureIsNotCompleted(t *testing.T) {
	ctx := context.Background()
	memory := conversation.NewMemory()
	s, _ := memory.Create(ctx, "alice")
	run, _ := NewStored(ctx, streamAgent([]*schema.AgenticMessage{testutil.Text("done")}), failingStore{memory}, "alice", s.ID).Start("hi")
	if last := drain(run); last.Kind != EventFailed || last.Err == nil {
		t.Fatalf("terminal=%+v", last)
	}
	lease, err := memory.Begin(ctx, "alice", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	turns, _ := memory.History(ctx, "alice", s.ID, 0, 10)
	if len(turns) != 0 {
		t.Fatal("failed commit was visible")
	}
}
