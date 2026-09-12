// Command memory-eval runs live DeepSeek checks against the conversation
// context, one-shot long-term profile, and ultra-long consolidation path.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"easygo-agent/internal/agent/chatmodel"
	deepagent "easygo-agent/internal/agent/deepagent.go"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/logger"
	"easygo-agent/internal/tools"
	"easygo-agent/internal/usermemory"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func main() {
	os.Exit(run())
}

func run() int {
	configPath := flag.String("config", "configs/memory-eval.yaml", "YAML configuration path")
	outPath := flag.String("out", "doc/memory-eval-report.md", "markdown report path")
	flag.Parse()
	if err := loadDotEnv(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "memory-eval: %v\n", err)
		return 1
	}
	if _, err := logger.New(logger.DefaultPath()); err != nil {
		fmt.Fprintf(os.Stderr, "memory-eval: %v\n", err)
		return 1
	}
	defer func() { _ = logger.Sync() }()

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	report, err := evaluate(ctx, *configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "memory-eval: %v\n", err)
		return 1
	}
	if err = os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "memory-eval: %v\n", err)
		return 1
	}
	if err = os.WriteFile(*outPath, []byte(renderReport(report)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "memory-eval: %v\n", err)
		return 1
	}
	fmt.Printf("wrote %s (%d/%d cases passed)\n", *outPath, report.Passed, report.Total)
	return 0
}

type evaluator struct {
	cfg        config.Config
	store      *conversation.Memory
	typedAgent adk.TypedAgent[*schema.AgenticMessage]
	memory     *usermemory.Service
	model      string
	started    time.Time
}

type criterion struct {
	Need []string
	Ban  []string
}

type caseResult struct {
	Tier       string
	Name       string
	Passed     bool
	Compressed bool
	Latency    time.Duration
	Answer     string
	Profile    []string
	Recalled   []string
	Need       []string
	Ban        []string
	HitNeed    []string
	HitBan     []string
	MissNeed   []string
	Notes      []string
	Error      string
}

type evalReport struct {
	Model   string
	Started time.Time
	Cases   []caseResult
	Passed  int
	Total   int
}

func evaluate(ctx context.Context, configPath string) (evalReport, error) {
	cfg, err := config.Load(configPath, os.LookupEnv)
	if err != nil {
		return evalReport{}, err
	}
	store := conversation.NewMemory()
	eval, err := newEvaluator(ctx, cfg, store)
	if err != nil {
		return evalReport{}, err
	}
	report := evalReport{Model: cfg.Model.Name, Started: time.Now().UTC()}
	report.Cases = append(report.Cases, eval.shortTerm(ctx))
	report.Cases = append(report.Cases, eval.singleRoundLongTerm(ctx))
	report.Cases = append(report.Cases, eval.ultraLongLongTerm(ctx))
	for _, item := range report.Cases {
		report.Total++
		if item.Passed {
			report.Passed++
		}
	}
	return report, nil
}

func newEvaluator(ctx context.Context, cfg config.Config, store *conversation.Memory) (*evaluator, error) {
	allTools, err := tools.NewAgentTool().AllTools(ctx)
	if err != nil {
		return nil, err
	}
	mainModel, err := chatmodel.New(ctx, cfg.Model)
	if err != nil {
		return nil, err
	}
	summaryModel, err := chatmodel.New(ctx, cfg.SubAgent)
	if err != nil {
		return nil, err
	}
	typed, err := deepagent.New(ctx, deepagent.Config{
		ChatModel:    mainModel,
		SummaryModel: summaryModel,
		Tools:        allTools,
		Agent:        cfg.Agent,
	})
	if err != nil {
		return nil, err
	}
	memoryAgent, err := usermemory.NewModelAgent(ctx, summaryModel)
	if err != nil {
		return nil, err
	}
	writer, err := usermemory.NewFileProfileWriter(cfg.Memory.StorageRoot)
	if err != nil {
		return nil, err
	}
	service, err := usermemory.NewService(store, store, memoryAgent, writer, cfg.Memory)
	if err != nil {
		return nil, err
	}
	return &evaluator{cfg: cfg, store: store, typedAgent: typed, memory: service, model: cfg.Model.Name, started: time.Now().UTC()}, nil
}

func (e *evaluator) shortTerm(ctx context.Context) caseResult {
	result := caseResult{
		Tier: "短期记忆（同会话上下文）",
		Name: "近期事实仍在会话窗口内",
		Need: []string{"夜莺", "3月18", "王敏"},
	}
	user := "eval-stm"
	session, err := e.store.Create(ctx, user)
	if err != nil {
		return fail(result, err)
	}
	setup := "请记住这些工作事实，后面我会提问：项目代号是夜莺，截止日期是3月18日，对接人是王敏。先简短确认。"
	if _, err = e.chat(ctx, user, session.ID, setup); err != nil {
		return fail(result, err)
	}
	return e.probe(ctx, result, user, session.ID, "请根据刚才的对话，依次写出：项目代号、截止日期、对接人。")
}

func (e *evaluator) singleRoundLongTerm(ctx context.Context) caseResult {
	result := caseResult{
		Tier: "单轮长期记忆（新会话召回）",
		Name: "一轮稳定偏好写入五槽档案后，空上下文仍能遵守",
		Need: []string{"白羽豆荚", "素食", "简体"},
	}
	user := "eval-ltm-single"
	session, err := e.store.Create(ctx, user)
	if err != nil {
		return fail(result, err)
	}
	setup := "请记住我的长期偏好：我是素食者；我对「白羽豆荚」严重过敏，任何推荐都不能包含它；对外文档必须用简体中文。先简短确认。"
	if _, err = e.chat(ctx, user, session.ID, setup); err != nil {
		return fail(result, err)
	}
	if err = e.memory.ConsolidateUser(ctx, user, time.Now().UTC().Add(time.Second)); err != nil {
		result.Notes = append(result.Notes, "consolidation: "+err.Error())
		return fail(result, err)
	}
	fresh, err := e.store.Create(ctx, user)
	if err != nil {
		return fail(result, err)
	}
	result.Profile = profileTexts(ctx, e.store, user)
	if !containsAll(strings.Join(result.Profile, "\n"), []string{"白羽豆荚"}) {
		result.Notes = append(result.Notes, "长期档案未写入关键过敏原，后续回答只能依赖模型临场猜测")
	}
	return e.probe(ctx, result, user, fresh.ID, "这是一个新会话。请根据你对我的长期记忆：1) 写出我过敏的食材名称；2) 列出三种符合我饮食习惯的加餐；3) 对外文档必须用什么语言？")
}

func (e *evaluator) ultraLongLongTerm(ctx context.Context) caseResult {
	result := caseResult{
		Tier: "超长轮长期记忆（压缩后归档再新开会话）",
		Name: "早期约束在长对话压缩后仍能进入档案并被新会话使用",
		Need: []string{"玄枢台账", "EG-7741", "周五"},
	}
	user := "eval-ltm-long"
	session, err := e.store.Create(ctx, user)
	if err != nil {
		return fail(result, err)
	}
	setup := "这些是必须长期遵守的内部事实：我们的内部工具叫「玄枢台账」；我的工号是 EG-7741；绝对禁止在周五把变更推到生产。先简短确认，不要展开。"
	first, err := e.chat(ctx, user, session.ID, setup)
	if err != nil {
		return fail(result, err)
	}
	result.Compressed = first.Compressed
	padding := strings.Repeat("这是一段与内部工具无关的填充说明，用于逐渐撑满会话上下文，请只回复「继续」。", 12)
	windowBroken := false
	for i := 0; i < 6; i++ {
		turn, chatErr := e.chat(ctx, user, session.ID, fmt.Sprintf("填充轮次 %d。%s", i+1, padding))
		if chatErr != nil {
			windowBroken = true
			result.Notes = append(result.Notes, fmt.Sprintf("填充轮次 %d 中断：%s", i+1, chatErr.Error()))
			break
		}
		result.Compressed = result.Compressed || turn.Compressed
	}
	if !windowBroken {
		sameSession := e.probe(ctx, caseResult{
			Tier: result.Tier,
			Name: "压缩后的同会话探针",
			Need: result.Need,
		}, user, session.ID, "我的工号是什么？内部工具叫什么？周五能不能把变更推到生产？")
		result.Notes = append(result.Notes, "同会话探针："+passLabel(sameSession.Passed))
		result.Compressed = result.Compressed || sameSession.Compressed
		if !sameSession.Passed {
			result.Notes = append(result.Notes, "压缩后的同会话短期召回已经失败")
		}
	} else {
		result.Notes = append(result.Notes, "会话窗口在填充阶段已经撑破，改为只检验长期档案能否在新会话恢复早期事实")
	}
	if err = e.memory.ConsolidateUser(ctx, user, time.Now().UTC().Add(time.Second)); err != nil {
		result.Notes = append(result.Notes, "consolidation: "+err.Error())
		return fail(result, err)
	}
	fresh, err := e.store.Create(ctx, user)
	if err != nil {
		return fail(result, err)
	}
	result.Profile = profileTexts(ctx, e.store, user)
	probed := e.probe(ctx, result, user, fresh.ID, "这是一个新会话。根据长期记忆回答：我的工号、内部工具名称，以及周五能否把变更推到生产？")
	if !result.Compressed && !windowBroken {
		probed.Notes = append(probed.Notes, "本轮未观察到 compressing 事件，上下文预算可能仍能装下全部填充文本")
	}
	return probed
}

type turnResult struct {
	Text       string
	Compressed bool
}

func (e *evaluator) chat(ctx context.Context, user, sessionID, input string) (turnResult, error) {
	fmt.Fprintf(os.Stderr, "run %s %s: %s\n", user, sessionID[:8], clip(input, 48))
	session := agentruntime.NewStored(ctx, e.typedAgent, e.store, user, sessionID, e.store)
	run, err := session.Start(input)
	if err != nil {
		return turnResult{}, err
	}
	defer run.Close()
	var result turnResult
	for {
		event := run.Next()
		if event.Kind == agentruntime.EventCompressing {
			result.Compressed = true
		}
		if !event.IsTerminal() {
			continue
		}
		if event.Kind == agentruntime.EventFailed {
			if event.Err != nil {
				return result, event.Err
			}
			return result, errors.New("agent run failed")
		}
		if event.Kind == agentruntime.EventCanceled {
			return result, context.Canceled
		}
		result.Text = event.Text
		return result, nil
	}
}

func (e *evaluator) probe(ctx context.Context, result caseResult, user, sessionID, prompt string) caseResult {
	started := time.Now()
	recalled, err := e.store.Recall(ctx, user, conversation.MaxProfileMemories)
	if err != nil {
		return fail(result, err)
	}
	result.Recalled = memoryTexts(recalled)
	turn, err := e.chat(ctx, user, sessionID, prompt)
	result.Latency = time.Since(started)
	if err != nil {
		return fail(result, err)
	}
	result.Answer = turn.Text
	result.Compressed = result.Compressed || turn.Compressed
	result.HitNeed, result.MissNeed = partitionFound(result.Answer, result.Need)
	result.HitBan = foundAll(result.Answer, result.Ban)
	result.Passed = len(result.MissNeed) == 0 && len(result.HitBan) == 0
	return result
}

func fail(result caseResult, err error) caseResult {
	result.Passed = false
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func profileTexts(ctx context.Context, store conversation.MemoryStore, user string) []string {
	profile, err := store.ActiveProfile(ctx, user)
	if err != nil {
		return []string{"profile error: " + err.Error()}
	}
	return memoryTexts(profile)
}

func memoryTexts(memories []conversation.LongTermMemory) []string {
	result := make([]string, 0, len(memories))
	for _, memory := range memories {
		result = append(result, fmt.Sprintf("%s %s", memory.Kind, memory.Content))
	}
	return result
}

func containsAll(text string, needles []string) bool {
	_, missing := partitionFound(text, needles)
	return len(missing) == 0
}

func partitionFound(text string, needles []string) (found, missing []string) {
	folded := fold(text)
	for _, needle := range needles {
		if needle == "" {
			continue
		}
		if strings.Contains(folded, fold(needle)) {
			found = append(found, needle)
			continue
		}
		missing = append(missing, needle)
	}
	return found, missing
}

func foundAll(text string, needles []string) []string {
	found, _ := partitionFound(text, needles)
	return found
}

func fold(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text)
}

func clip(text string, n int) string {
	runes := []rune(strings.ReplaceAll(text, "\n", " "))
	if len(runes) <= n {
		return string(runes)
	}
	return string(runes[:n]) + "…"
}

func passLabel(ok bool) string {
	if ok {
		return "通过"
	}
	return "未通过"
}

func renderReport(report evalReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 记忆系统评测结果\n\n")
	fmt.Fprintf(&b, "- 模型：`%s`\n", report.Model)
	fmt.Fprintf(&b, "- 时间：%s\n", report.Started.Format(time.RFC3339))
	fmt.Fprintf(&b, "- 通过：%d / %d\n\n", report.Passed, report.Total)
	fmt.Fprintf(&b, "| 档位 | 用例 | 结果 | 压缩 | 耗时 |\n| --- | --- | --- | --- | --- |\n")
	for _, item := range report.Cases {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", item.Tier, item.Name, passLabel(item.Passed), yesNo(item.Compressed), item.Latency.Round(time.Millisecond))
	}
	b.WriteString("\n")
	for _, item := range report.Cases {
		fmt.Fprintf(&b, "## %s\n\n", item.Tier)
		fmt.Fprintf(&b, "%s。\n\n", item.Name)
		fmt.Fprintf(&b, "- 结果：%s\n", passLabel(item.Passed))
		if item.Error != "" {
			fmt.Fprintf(&b, "- 错误：`%s`\n", item.Error)
		}
		if len(item.HitNeed) > 0 {
			fmt.Fprintf(&b, "- 命中：%s\n", strings.Join(item.HitNeed, "、"))
		}
		if len(item.MissNeed) > 0 {
			fmt.Fprintf(&b, "- 缺失：%s\n", strings.Join(item.MissNeed, "、"))
		}
		if len(item.HitBan) > 0 {
			fmt.Fprintf(&b, "- 误伤禁词：%s\n", strings.Join(item.HitBan, "、"))
		}
		if len(item.Recalled) > 0 {
			fmt.Fprintf(&b, "- 探针前召回：\n")
			for _, line := range item.Recalled {
				fmt.Fprintf(&b, "  - %s\n", line)
			}
		} else {
			b.WriteString("- 探针前召回：空\n")
		}
		if len(item.Profile) > 0 {
			fmt.Fprintf(&b, "- 长期档案：\n")
			for _, line := range item.Profile {
				fmt.Fprintf(&b, "  - %s\n", line)
			}
		}
		for _, note := range item.Notes {
			fmt.Fprintf(&b, "- 备注：%s\n", note)
		}
		if strings.TrimSpace(item.Answer) != "" {
			fmt.Fprintf(&b, "\n探针回答：\n\n```text\n%s\n```\n\n", clip(item.Answer, 1200))
		}
	}
	b.WriteString("## 结论\n\n")
	b.WriteString(conclusion(report))
	b.WriteString("\n")
	return b.String()
}

func conclusion(report evalReport) string {
	if report.Total == 0 {
		return "没有跑出任何用例。"
	}
	var lines []string
	for _, item := range report.Cases {
		switch {
		case item.Tier == "短期记忆（同会话上下文）" && item.Passed:
			lines = append(lines, "短期记忆：同会话窗口能保留刚说的工作事实，会话上下文路径可用。")
		case item.Tier == "短期记忆（同会话上下文）":
			lines = append(lines, "短期记忆：同会话问答没有稳定复述关键事实，优先查会话提交和模型是否改写了历史。")
		case strings.Contains(item.Tier, "单轮") && item.Passed:
			lines = append(lines, "单轮长期记忆：一轮对话经 consolidation 后，新会话仍能遵守档案约束。")
		case strings.Contains(item.Tier, "单轮"):
			lines = append(lines, "单轮长期记忆失败：要么提取/和解没有写入五槽档案，要么召回注入了但模型没有遵守。")
		case strings.Contains(item.Tier, "超长") && item.Passed:
			lines = append(lines, "超长轮长期记忆：长对话之后关键约束仍能进入档案并在新会话生效。")
		case strings.Contains(item.Tier, "超长"):
			lines = append(lines, "超长轮长期记忆失败：填充对话可能冲掉提取信号，或压缩摘要丢掉了早期约束。")
		}
	}
	if report.Passed == report.Total {
		lines = append(lines, "三档均通过，当前记忆系统在本次 deepseek-flash 抽样下可以把会话上下文和五槽长期档案接到同一条回答路径上。")
	} else {
		lines = append(lines, fmt.Sprintf("本次抽样通过率为 %d/%d，长期记忆是否可用取决于提取质量和五槽排名，而不是只看会话窗口。", report.Passed, report.Total))
	}
	return strings.Join(lines, "\n\n")
}

func yesNo(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, strings.Trim(strings.TrimSpace(value), `"'`)); err != nil {
			return err
		}
	}
	return scanner.Err()
}
