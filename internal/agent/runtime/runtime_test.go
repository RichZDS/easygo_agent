package agentruntime

import (
	"context"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
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

func mustClaim(t *testing.T, ctx context.Context, store conversation.QueueStore, agent adk.TypedAgent[*schema.AgenticMessage], user, sessionID, input string, memory ...conversation.MemoryStore) Run {
	t.Helper()
	run, err := ClaimQueuedRun(ctx, store, agent, user, sessionID, input, memory...)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestNativeStreamingChunksAreConcatenated(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	s, _ := store.Create(ctx, "alice")
	agent := streamAgent([]*schema.AgenticMessage{reasoningChunk(0, "think "), reasoningChunk(0, "done"), assistantTextChunk(1, "hel"), assistantTextChunk(1, "lo")})
	run := mustClaim(t, ctx, store, agent, "alice", s.ID, "hi")
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
	run := mustClaim(t, ctx, store, agent, "alice", s.ID, "hi")
	if e := run.Next(); e.Kind != EventTextDelta {
		t.Fatalf("first=%+v", e)
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

type failingQueueStore struct{ conversation.QueueStore }
type failingRunLease struct{ conversation.RunLease }

func (s failingQueueStore) ClaimNext(ctx context.Context, workerID string, ttl time.Duration) (conversation.RunLease, error) {
	lease, err := s.QueueStore.ClaimNext(ctx, workerID, ttl)
	if err != nil {
		return nil, err
	}
	return failingRunLease{lease}, nil
}
func (l failingRunLease) CommitRun(context.Context, []*schema.AgenticMessage, conversation.RunStatus, []*schema.AgenticMessage, string, string) error {
	return errors.New("disk unavailable")
}
func TestQueuedRunProjectsToolLifecycle(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	s, _ := store.Create(ctx, "alice")
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{
		schema.NewContentBlock(&schema.FunctionToolCall{Name: "calculator", CallID: "c1", Arguments: `{"a":2}`}),
		schema.NewContentBlock(&schema.FunctionToolResult{Name: "calculator", CallID: "c1", Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "4"}}}}),
	}}
	run := mustClaim(t, ctx, store, streamAgent([]*schema.AgenticMessage{message}), "alice", s.ID, "hi")
	var started, finished Event
	for {
		event := run.Next()
		switch event.Kind {
		case EventToolStarted:
			started = event
		case EventToolFinished:
			finished = event
		}
		if event.Kind == EventCompleted || event.Kind == EventFailed || event.Kind == EventCanceled {
			break
		}
	}
	if started.Tool != "calculator" || started.CallID != "c1" || started.Arguments != `{"a":2}` {
		t.Fatalf("tool started=%+v", started)
	}
	if finished.Tool != "calculator" || finished.CallID != "c1" || finished.Result != "4" {
		t.Fatalf("tool finished=%+v", finished)
	}
}

func TestQueuedRunProjectsCompressionActions(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	s, _ := store.Create(ctx, "alice")
	agent := &scriptedAgent{run: func(context.Context, *adk.TypedAgentInput[*schema.AgenticMessage]) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
		iter, gen := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
		go func() {
			defer gen.Close()
			gen.Send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Action: &adk.AgentAction{CustomizedAction: &summarization.TypedCustomizedAction[*schema.AgenticMessage]{Type: summarization.ActionTypeBeforeSummarize}}})
			gen.Send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Action: &adk.AgentAction{CustomizedAction: &summarization.TypedCustomizedAction[*schema.AgenticMessage]{Type: summarization.ActionTypeAfterSummarize}}})
			gen.Send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Output: &adk.TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: &adk.TypedMessageVariant[*schema.AgenticMessage]{Message: testutil.Text("after")}}})
		}()
		return iter
	}}
	run := mustClaim(t, ctx, store, agent, "alice", s.ID, "hi")
	var kinds []EventKind
	for {
		event := run.Next()
		kinds = append(kinds, event.Kind)
		if event.Kind == EventCompleted || event.Kind == EventFailed || event.Kind == EventCanceled {
			break
		}
	}
	if len(kinds) < 3 || kinds[0] != EventCompressing || kinds[1] != EventCompressed {
		t.Fatalf("kinds=%v", kinds)
	}
}

func TestCommitFailureIsNotCompleted(t *testing.T) {
	ctx := context.Background()
	memory := conversation.NewMemory()
	s, _ := memory.Create(ctx, "alice")
	run := mustClaim(t, ctx, failingQueueStore{memory}, streamAgent([]*schema.AgenticMessage{testutil.Text("done")}), "alice", s.ID, "hi")
	if last := drain(run); last.Kind != EventFailed || last.Err == nil {
		t.Fatalf("terminal=%+v", last)
	}
	turns, _ := memory.History(ctx, "alice", s.ID, 0, 10)
	if len(turns) != 0 {
		t.Fatal("failed commit was visible")
	}
}
