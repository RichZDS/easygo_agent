package deepagent

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"easygo-agent/internal/testutil"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
)

func TestRejectRepeatedFillerCatchesPadding(t *testing.T) {
	err := RejectRepeatedFiller([]string{
		strings.Repeat("这是一段与内部工具无关的填充说明，用于逐渐撑满会话上下文，请只回复「继续」。", 12),
		strings.Repeat("另一段同样手法的填充说明，用于逐渐撑满会话上下文，请只回复「继续」。", 12),
	})
	if err == nil {
		t.Fatal("一段话*n padding must fail the corpus check")
	}
}

func TestThemedCorpusIsNotRepeatedFiller(t *testing.T) {
	turns := ThemedUserTurns()
	if err := RejectRepeatedFiller(turns); err != nil {
		t.Fatal(err)
	}
	for i, turn := range ThemedReleaseDialogue() {
		n := utf8.RuneCountInString(turn.User) + utf8.RuneCountInString(turn.Assistant)
		if n < 360 {
			t.Fatalf("round %d is too short (%d runes)", i, n)
		}
	}
}

func TestFitShrinksHeuristicBeforeDialogueAndKeepsThemeFacts(t *testing.T) {
	req := themedOverBudgetRequest(t)
	before, err := Measure(req.Messages, req.Tools)
	if err != nil {
		t.Fatal(err)
	}
	if before.Total <= req.Limit {
		t.Fatalf("fixture is not over budget: total=%d limit=%d", before.Total, req.Limit)
	}
	opt, err := Fit(req)
	if err != nil {
		t.Fatal(err)
	}
	base, err := FitBaseline(req)
	if err != nil {
		t.Fatal(err)
	}
	if !opt.Fitted {
		t.Fatalf("optimized path still over budget: after=%d limit=%d", opt.After.Total, req.Limit)
	}
	if !opt.SkillShrunk {
		t.Fatal("heuristic skill catalog was not compacted")
	}
	if !containsName(opt.DroppedTools, heuristicLoadSkill) || !containsName(opt.DroppedTools, heuristicCallTool) {
		t.Fatalf("heuristic tools not dropped: %v", opt.DroppedTools)
	}
	if opt.DialogueSummarizedBytes >= base.DialogueSummarizedBytes {
		t.Fatalf("optimized summarized %d bytes of dialogue, baseline %d; expected less", opt.DialogueSummarizedBytes, base.DialogueSummarizedBytes)
	}
	blob := messagesBlob(opt.Messages)
	for _, fact := range []string{themeToolName, themeStaffID, themeFriday} {
		if !strings.Contains(blob, fact) {
			t.Fatalf("lost theme fact %q in rewritten messages:\n%s", fact, blob)
		}
	}
}

func TestBeforeModelRewriteStateUsesFitNotWholesaleSummary(t *testing.T) {
	req := themedOverBudgetRequest(t)
	summarizerCalled := false
	summary := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		summarizerCalled = true
		return testutil.Text("wholesale summary should not run"), nil
	}}
	ctx := context.Background()
	mw, err := newCompression(ctx, summary, req.Limit)
	if err != nil {
		t.Fatal(err)
	}
	native, err := summarization.NewTyped(ctx, &summarization.TypedConfig[*schema.AgenticMessage]{
		Model: summary, Trigger: &summarization.TriggerCondition{ContextTokens: req.Limit}, TokenCounter: countInputTokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	guard := mw.(*compressionBudget)
	guard.TypedChatModelAgentMiddleware = native
	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: req.Messages, ToolInfos: req.Tools}
	_, next, err := guard.BeforeModelRewriteState(ctx, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summarizerCalled {
		t.Fatal("summarizer ran; skill/tool shrink should have avoided wholesale dialogue dump")
	}
	tokens, err := countInputTokens(ctx, &summarization.TypedTokenCounterInput[*schema.AgenticMessage]{Messages: next.Messages, Tools: next.ToolInfos})
	if err != nil {
		t.Fatal(err)
	}
	if tokens > req.Limit {
		t.Fatalf("rewritten context %d exceeds budget %d", tokens, req.Limit)
	}
	blob := messagesBlob(next.Messages)
	for _, fact := range []string{themeToolName, themeStaffID, themeFriday} {
		if !strings.Contains(blob, fact) {
			t.Fatalf("rewrite lost %q", fact)
		}
	}
	if containsTool(next.ToolInfos, heuristicLoadSkill) || containsTool(next.ToolInfos, heuristicCallTool) {
		t.Fatalf("heuristic tools still bound: %v", toolNames(next.ToolInfos))
	}
}

func TestBuildBudgetReportUsesShippedFitNumbers(t *testing.T) {
	req := themedOverBudgetRequest(t)
	report, err := BuildBudgetReport(req, []string{themeToolName, themeStaffID, themeFriday})
	if err != nil {
		t.Fatal(err)
	}
	md := RenderBudgetMarkdown(report)
	if !strings.Contains(md, strconv.Itoa(report.Before.Total)) || !strings.Contains(md, strconv.Itoa(report.After.Total)) {
		t.Fatalf("report missing shipped totals: %s", md)
	}
	if report.Optimized.DialogueSummarizedBytes >= report.Baseline.DialogueSummarizedBytes {
		t.Fatal("report baseline/optimized dialogue bytes are not an improvement")
	}
	if len(report.FactsKept) != 3 {
		t.Fatalf("report lost theme facts: %v", report.FactsKept)
	}
}

func themedOverBudgetRequest(t *testing.T) FitRequest {
	t.Helper()
	req, err := NewThemedFitRequest()
	if err != nil {
		t.Fatal(err)
	}
	before, err := Measure(req.Messages, req.Tools)
	if err != nil {
		t.Fatal(err)
	}
	if before.Total <= req.Limit {
		t.Fatalf("could not size over-budget fixture: total=%d limit=%d", before.Total, req.Limit)
	}
	return req
}

func messagesBlob(messages []*schema.AgenticMessage) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(messageText(message))
		b.WriteByte('\n')
	}
	return b.String()
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func containsTool(tools []*schema.ToolInfo, name string) bool {
	for _, tool := range tools {
		if tool != nil && tool.Name == name {
			return true
		}
	}
	return false
}
