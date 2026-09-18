package deepagent

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	agentruntime "easygo-agent/internal/agent/runtime"

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
	if containsName(opt.DroppedTools, heuristicLoadSkill) || containsName(opt.DroppedTools, heuristicCallTool) {
		t.Fatalf("discovery tools were dropped: %v", opt.DroppedTools)
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

func TestFitTokenCounterAgreesWithMeasure(t *testing.T) {
	req := themedOverBudgetRequest(t)
	before, err := Measure(req.Messages, req.Tools)
	if err != nil {
		t.Fatal(err)
	}
	counted, err := countInputTokens(context.Background(), &summarization.TypedTokenCounterInput[*schema.AgenticMessage]{Messages: req.Messages, Tools: req.Tools})
	if err != nil {
		t.Fatal(err)
	}
	if counted != max(before.Total, providerUsage(req.Messages)) {
		t.Fatalf("token counter %d disagrees with Measure %d (usage=%d)", counted, before.Total, providerUsage(req.Messages))
	}
	opt, err := Fit(req)
	if err != nil {
		t.Fatal(err)
	}
	afterCounted, err := countInputTokens(context.Background(), &summarization.TypedTokenCounterInput[*schema.AgenticMessage]{Messages: opt.Messages, Tools: opt.Tools})
	if err != nil {
		t.Fatal(err)
	}
	if afterCounted != max(opt.After.Total, providerUsage(opt.Messages)) {
		t.Fatalf("fitted token counter %d disagrees with Fit.After %d", afterCounted, opt.After.Total)
	}
	if !opt.Fitted {
		t.Fatal("expected Fit to bring the themed fixture under budget without a summarizer")
	}
	if !containsTool(opt.Tools, heuristicLoadSkill) || !containsTool(opt.Tools, heuristicCallTool) {
		t.Fatalf("discovery tools lost: %v", opt.KeptTools)
	}
}

func TestThemeNotesSurviveRuntimeSystemDrop(t *testing.T) {
	req := themedOverBudgetRequest(t)
	opt, err := Fit(req)
	if err != nil {
		t.Fatal(err)
	}
	if !opt.Fitted {
		t.Fatalf("optimized path still over budget: after=%d limit=%d", opt.After.Total, req.Limit)
	}
	if opt.ThemeNotes == "" || opt.SummarizedMessages == 0 {
		t.Fatal("expected extracted theme notes for the over-budget fixture")
	}
	var noteRole schema.AgenticRoleType
	for _, message := range opt.Messages {
		if message != nil && messageText(message) == opt.ThemeNotes {
			noteRole = message.Role
		}
	}
	if noteRole != schema.AgenticRoleTypeUser {
		t.Fatalf("theme notes role=%q, want user so initialize() will keep them", noteRole)
	}
	kept := agentruntime.DropPersistedSystemMessages(opt.Messages)
	for _, message := range kept {
		if message != nil && message.Role == schema.AgenticRoleTypeSystem {
			t.Fatal("initialize filter left a system message")
		}
	}
	blob := messagesBlob(kept)
	for _, fact := range []string{themeToolName, themeStaffID, themeFriday} {
		if !strings.Contains(blob, fact) {
			t.Fatalf("after initialize() system drop, lost theme fact %q in:\n%s", fact, blob)
		}
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

func TestCompressionPreservesTaskControlAndUnfinishedSummary(t *testing.T) {
	names := []string{"list_skills", "load_skill", "read_skill_resource", "call_tool", "spawn_subagent", "get_task", "list_tasks", "resume_task", "cancel_task", "update_plan"}
	infos := []*schema.ToolInfo{}
	for _, name := range names {
		infos = append(infos, &schema.ToolInfo{Name: name, Desc: "capability"})
	}
	req := FitRequest{Messages: []*schema.AgenticMessage{schema.SystemAgenticMessage("Unfinished background tasks: task-1 running; preserve acceptance: evidence"), schema.UserAgenticMessage(strings.Repeat("earlier dialogue ", 400)), schema.UserAgenticMessage("hello")}, Tools: infos, Limit: 800}
	got, err := Fit(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !containsTool(got.Tools, name) {
			t.Fatalf("lost %s", name)
		}
	}
	if !strings.Contains(messagesBlob(got.Messages), "task-1 running") {
		t.Fatal("lost unfinished tasks")
	}
}
