package deepagent

import (
	"context"
	"easygo-agent/internal/testutil"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
)

func TestBudgetIncludesChineseAndToolSchema(t *testing.T) {
	tokens, err := countInputTokens(context.Background(), &summarization.TypedTokenCounterInput[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{schema.UserAgenticMessage(strings.Repeat("中文", 100))}, Tools: []*schema.ToolInfo{{Name: "t", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"long_argument": {Type: schema.String, Desc: strings.Repeat("schema", 100)}})}}})
	if err != nil {
		t.Fatal(err)
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
	mw, err := newCompression(ctx, summary, 2000)
	if err != nil {
		t.Fatal(err)
	}
	// Use the native middleware without internal events for a standalone hook test.
	native, err := summarization.NewTyped(ctx, &summarization.TypedConfig[*schema.AgenticMessage]{Model: summary, Trigger: &summarization.TriggerCondition{ContextTokens: 2000}, TokenCounter: countInputTokens})
	if err != nil {
		t.Fatal(err)
	}
	guard := mw.(*compressionBudget)
	guard.TypedChatModelAgentMiddleware = native
	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{schema.UserAgenticMessage(strings.Repeat("history ", 2000))}}
	if _, _, err := guard.BeforeModelRewriteState(ctx, state, nil); err == nil || !strings.Contains(err.Error(), "exceeds input budget") {
		t.Fatalf("oversized summary accepted: %v", err)
	}
}
