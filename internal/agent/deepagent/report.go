package deepagent

import (
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// BudgetReport is the before/after snapshot produced from Fit numbers.
type BudgetReport struct {
	Started   time.Time
	Theme     string
	Limit     int
	Before    Composition
	After     Composition
	Baseline  FitResult
	Optimized FitResult
	Facts     []string
	FactsKept []string
}

// BuildBudgetReport runs baseline vs shipped Fit on the same request.
func BuildBudgetReport(req FitRequest, facts []string) (BudgetReport, error) {
	opt, err := Fit(req)
	if err != nil {
		return BudgetReport{}, err
	}
	base, err := FitBaseline(req)
	if err != nil {
		return BudgetReport{}, err
	}
	kept := survivingFacts(opt.Messages, facts)
	return BudgetReport{
		Started:   time.Now().UTC(),
		Theme:     "玄枢台账发布窗口（工号 EG-7741，周五生产冻结）",
		Limit:     req.Limit,
		Before:    opt.Before,
		After:     opt.After,
		Baseline:  base,
		Optimized: opt,
		Facts:     facts,
		FactsKept: kept,
	}, nil
}

func survivingFacts(messages []*schema.AgenticMessage, facts []string) []string {
	blob := ""
	for _, message := range messages {
		blob += messageText(message)
	}
	var kept []string
	for _, fact := range facts {
		if strings.Contains(blob, fact) {
			kept = append(kept, fact)
		}
	}
	return kept
}

// RenderBudgetMarkdown is the 优化前后报告 body, from shipped Fit numbers.
func RenderBudgetMarkdown(report BudgetReport) string {
	var b strings.Builder
	b.WriteString("# 超长上下文压缩优化前后报告\n\n")
	fmt.Fprintf(&b, "- 时间：%s\n", report.Started.Format(time.RFC3339))
	fmt.Fprintf(&b, "- 主题：%s\n", report.Theme)
	fmt.Fprintf(&b, "- 输入预算 `agent.context_tokens`：%d\n", report.Limit)
	b.WriteString("- 语料：多轮真实主题对话（玄枢台账发布），每轮独立长文本，禁止一段话*n。\n\n")
	b.WriteString("## 优化前（当前路径的构成）\n\n")
	b.WriteString("| 构成 | token 估计（UTF-8 字节 + 256 帧） |\n| --- | ---: |\n")
	fmt.Fprintf(&b, "| 对话消息 | %d |\n", report.Before.Dialogue)
	fmt.Fprintf(&b, "| 启发式 skill 目录 | %d |\n", report.Before.Skill)
	fmt.Fprintf(&b, "| 工具 schema | %d |\n", report.Before.Tools)
	fmt.Fprintf(&b, "| 合计 | **%d** |\n\n", report.Before.Total)
	fmt.Fprintf(&b, "相对预算超支 %d（%.1f%%）。\n\n", report.Before.Total-report.Limit, pct(report.Before.Total, report.Limit))
	b.WriteString("旧路径会把**整段对话**交给摘要模型，且不缩小 skill/tool。基线统计：\n\n")
	fmt.Fprintf(&b, "- 被整段摘要的对话字节：%d（%d 条消息）\n", report.Baseline.DialogueSummarizedBytes, report.Baseline.SummarizedMessages)
	fmt.Fprintf(&b, "- skill/tool 是否缩小：否 / 否\n")
	fmt.Fprintf(&b, "- 摘要后能否装进预算：%s（合计 %d）\n\n", yesNoCN(report.Baseline.Fitted), report.Baseline.After.Total)
	b.WriteString("## 优化后（先缩启发式，再按形势抽取旧对话）\n\n")
	b.WriteString("| 构成 | 优化前 | 优化后 | 变化 |\n| --- | ---: | ---: | ---: |\n")
	fmt.Fprintf(&b, "| 对话 | %d | %d | %+d |\n", report.Before.Dialogue, report.After.Dialogue, report.After.Dialogue-report.Before.Dialogue)
	fmt.Fprintf(&b, "| 启发式 skill | %d | %d | %+d |\n", report.Before.Skill, report.After.Skill, report.After.Skill-report.Before.Skill)
	fmt.Fprintf(&b, "| 工具 schema | %d | %d | %+d |\n", report.Before.Tools, report.After.Tools, report.After.Tools-report.Before.Tools)
	fmt.Fprintf(&b, "| 合计 | %d | **%d** | %+d |\n\n", report.Before.Total, report.After.Total, report.After.Total-report.Before.Total)
	fmt.Fprintf(&b, "- 是否触发对话摘要/抽取：%s（抽取/替换 %d 条旧消息，%d 字节）\n", yesNoCN(report.Optimized.SummarizedMessages > 0), report.Optimized.SummarizedMessages, report.Optimized.DialogueSummarizedBytes)
	fmt.Fprintf(&b, "- 完整保留的对话字节：%d\n", report.Optimized.DialogueKeptBytes)
	fmt.Fprintf(&b, "- 相对基线少摘要的对话字节：%d\n", report.Baseline.DialogueSummarizedBytes-report.Optimized.DialogueSummarizedBytes)
	fmt.Fprintf(&b, "- skill 目录是否压缩：%s\n", yesNoCN(report.Optimized.SkillShrunk))
	fmt.Fprintf(&b, "- 去掉的启发式工具：%s\n", emptyDash(strings.Join(report.Optimized.DroppedTools, ", ")))
	fmt.Fprintf(&b, "- 保留的工具：%s\n", emptyDash(strings.Join(report.Optimized.KeptTools, ", ")))
	fmt.Fprintf(&b, "- 装进预算：%s\n\n", yesNoCN(report.Optimized.Fitted))
	b.WriteString("## 主题事实是否还在\n\n")
	for _, fact := range report.Facts {
		status := "缺失"
		for _, kept := range report.FactsKept {
			if kept == fact {
				status = "保留"
				break
			}
		}
		fmt.Fprintf(&b, "- `%s`：%s\n", fact, status)
	}
	b.WriteString("\n## 结论\n\n")
	if report.Optimized.Fitted && len(report.FactsKept) == len(report.Facts) && report.Optimized.DialogueSummarizedBytes < report.Baseline.DialogueSummarizedBytes {
		b.WriteString("优化后先丢掉当前形势用不上的启发式 skill/tool，再只抽取旧对话里和主题重叠的句子，因此对话被摘要的量小于旧路径，且早期约束（玄枢台账 / EG-7741 / 周五冻结）仍留在改写后的上下文中。\n")
	} else {
		b.WriteString("优化路径已记录上述数字。若未装进预算或主题事实缺失，应继续收紧抽取而不是静默截断。\n")
	}
	return b.String()
}

// RenderBudgetHTML is a readable companion to the markdown report.
func RenderBudgetHTML(report BudgetReport) string {
	md := RenderBudgetMarkdown(report)
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<title>超长上下文压缩优化前后</title>`)
	b.WriteString(`<style>
:root{--bg:#f4efe6;--paper:#fffaf2;--ink:#1c1915;--muted:#6b645b;--line:#e3d8c6;--accent:#c45c26;--pass:#1f6b45;--pass-bg:#e5f4ea}
html,body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.65 "Iowan Old Style","Songti SC",serif}
.wrap{max-width:900px;margin:0 auto;padding:48px 24px 80px}
h1{font-size:36px;margin:0 0 12px}
.kicker{letter-spacing:.18em;text-transform:uppercase;font-size:12px;color:var(--accent);font-family:"Segoe UI",sans-serif}
.meta{display:grid;grid-template-columns:repeat(4,1fr);gap:12px;margin:28px 0}
.stat{background:var(--paper);border:1px solid var(--line);padding:14px 16px}
.stat b{display:block;font-size:20px}
.stat span{color:var(--muted);font-size:13px;font-family:"Segoe UI",sans-serif}
table{width:100%;border-collapse:collapse;background:var(--paper);margin:12px 0 24px}
th,td{border:1px solid var(--line);padding:10px 12px;text-align:left}
th{background:#f0e6d4;font-family:"Segoe UI",sans-serif;font-size:13px}
.ok{background:var(--pass-bg);color:var(--pass);padding:2px 8px;font-family:"Segoe UI",sans-serif;font-size:12px}
pre{white-space:pre-wrap;background:var(--paper);border:1px solid var(--line);padding:16px}
@media(max-width:800px){.meta{grid-template-columns:1fr 1fr}}
</style></head><body><div class="wrap">`)
	fmt.Fprintf(&b, `<p class="kicker">Memory compression</p><h1>超长上下文压缩优化前后</h1>`)
	fmt.Fprintf(&b, `<div class="meta"><div class="stat"><b>%d</b><span>优化前合计</span></div><div class="stat"><b>%d</b><span>优化后合计</span></div><div class="stat"><b>%d</b><span>少摘要的对话字节</span></div><div class="stat"><b>%s</b><span>装进预算</span></div></div>`,
		report.Before.Total, report.After.Total, report.Baseline.DialogueSummarizedBytes-report.Optimized.DialogueSummarizedBytes, yesNoCN(report.Optimized.Fitted))
	b.WriteString("<pre>")
	b.WriteString(htmlEscape(md))
	b.WriteString("</pre></div></body></html>")
	return b.String()
}

func htmlEscape(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(text)
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d) * 100
}

func yesNoCN(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func emptyDash(text string) string {
	if strings.TrimSpace(text) == "" {
		return "（无）"
	}
	return text
}
