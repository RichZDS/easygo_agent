package deepagent

import (
	"context"
	"strings"
	"testing"

	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"

	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
)

func TestBudgetIncludesChineseAndToolSchema(t *testing.T) {
	messages := []*schema.AgenticMessage{schema.UserAgenticMessage(strings.Repeat("中文", 100))}
	tools := []*schema.ToolInfo{{Name: "t", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"long_argument": {Type: schema.String, Desc: strings.Repeat("schema", 100)}})}}
	measured, err := Measure(messages, tools)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := countInputTokens(context.Background(), &summarization.TypedTokenCounterInput[*schema.AgenticMessage]{Messages: messages, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if tokens != measured.Total {
		t.Fatalf("token counter %d disagrees with Measure %d", tokens, measured.Total)
	}
	if tokens < 1200 {
		t.Fatalf("undercounted Chinese or tool schema: %d", tokens)
	}
}

func TestOversizedSummaryIsRejected(t *testing.T) {
	ctx := context.Background()
	summary := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		return testutil.Text(strings.Repeat("long summary ", 2000)), nil
	}}
	main := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		t.Fatal("main model ran after an oversized summary")
		return testutil.Text("should not run"), nil
	}}
	agent, err := New(ctx, Config{ChatModel: main, SummaryModel: summary, Agent: config.AgentConfig{MaxSteps: 2, ContextTokens: 2000}})
	if err != nil {
		t.Fatal(err)
	}
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	run, err := agentruntime.ClaimQueuedRun(ctx, store, agent, "alice", session.ID, strings.Repeat("history ", 2000))
	if err != nil {
		t.Fatal(err)
	}
	last := drainRun(t, run)
	if last.Kind != agentruntime.EventFailed || last.Err == nil || !strings.Contains(last.Err.Error(), "exceeds input budget") {
		t.Fatalf("oversized summary accepted: %+v", last)
	}
}

func drainRun(t *testing.T, run agentruntime.Run) agentruntime.Event {
	t.Helper()
	for i := 0; i < 100; i++ {
		event := run.Next()
		if event.Kind == agentruntime.EventCompleted || event.Kind == agentruntime.EventFailed || event.Kind == agentruntime.EventCanceled {
			return event
		}
	}
	t.Fatal("run did not terminate")
	return agentruntime.Event{}
}
