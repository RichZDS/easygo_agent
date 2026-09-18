// Command skill-eval checks that the agent sees only the catalog heuristic and
// loads specialized skills through load_skill.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"go.uber.org/zap"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"easygo-agent/internal/agent/chatmodel"
	"easygo-agent/internal/agent/deepagent"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/logger"
	"easygo-agent/internal/prompt"
	"easygo-agent/internal/skill"
	"easygo-agent/internal/tools"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func main() { os.Exit(run()) }

func run() int {
	configPath := flag.String("config", "configs/eval/skill.yaml", "YAML configuration path")
	outPath := flag.String("out", "doc/eval/skill-framework.md", "markdown report path")
	baselinePath := flag.String("baseline", "", "optional JSON result from an earlier checkout for comparison")
	flag.Parse()
	if err := loadDotEnv(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "skill-eval: %v\n", err)
		return 1
	}
	if _, err := logger.New(logger.DefaultPath(), zap.DebugLevel); err != nil {
		fmt.Fprintf(os.Stderr, "skill-eval: %v\n", err)
		return 1
	}
	defer func() { _ = logger.Sync() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	report, err := evaluate(ctx, *configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skill-eval: %v\n", err)
		return 1
	}
	if err = os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "skill-eval: %v\n", err)
		return 1
	}
	if err = os.WriteFile(*outPath, []byte(render(report)+compareReport(*baselinePath, report)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "skill-eval: %v\n", err)
		return 1
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(*outPath+".json", data, 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("wrote %s (%d/%d cases passed)\n", *outPath, report.Passed, report.Total)
	if report.Passed != report.Total {
		return 1
	}
	return 0
}

type caseSpec struct {
	Name         string
	Prompt       string
	NeedSkill    string
	ExtraSkills  []string
	NeedResource bool
	NeedTool     string
	NeedText     string
	ForbidSkill  bool
}

type caseResult struct {
	LoadedNames []string
	ModelCalls  int64
	InputBytes  int64
	Name        string
	Passed      bool
	Tools       []string
	Answer      string
	Error       string
	Need        string
	Loaded      bool
	Latency     time.Duration
}

type evalReport struct {
	Catalog string
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
	lib, err := skill.Open(skill.DefaultRoot)
	if err != nil {
		return evalReport{}, err
	}
	allTools, err := tools.NewAgentTool().WithSkills(lib).AllTools(ctx)
	if err != nil {
		return evalReport{}, err
	}
	mainModel, err := chatmodel.New(ctx, cfg.Model)
	if err != nil {
		return evalReport{}, err
	}
	summaryModel, err := chatmodel.New(ctx, cfg.SubAgent)
	if err != nil {
		return evalReport{}, err
	}
	meter := &meterModel{AgenticModel: mainModel}
	agent, err := deepagent.New(ctx, deepagent.Config{
		ChatModel:    meter,
		SummaryModel: summaryModel,
		Tools:        allTools,
		Agent:        cfg.Agent,
		Instruction:  prompt.WithSkillCatalog(prompt.SystemPrompt, lib.CatalogPrompt()),
	})
	if err != nil {
		return evalReport{}, err
	}
	store := conversation.NewMemory()
	report := evalReport{Catalog: lib.CatalogPrompt(), Started: time.Now().UTC()}
	for _, spec := range []caseSpec{
		{Name: "算术走目录再加载", Prompt: "请计算 17 乘以 34。", NeedSkill: "strict-arithmetic", NeedTool: "calculator", NeedText: "CALC:578"},
		{Name: "口令走目录再加载", Prompt: "请重复口令 ALPHA-ROSE-9", NeedSkill: "bracket-token-reply", NeedText: "[[ALPHA-ROSE-9]]"},
		{Name: "显式 skill 触发", Prompt: "使用 $strict-arithmetic 算 12 加 7。", NeedSkill: "strict-arithmetic", NeedTool: "calculator", NeedText: "CALC:19"},
		{Name: "多个 skill 组合", Prompt: "使用 $strict-arithmetic 和 $bracket-token-reply，计算 2 加 3，并重复口令 COMBO-7。", NeedSkill: "strict-arithmetic", ExtraSkills: []string{"bracket-token-reply"}, NeedTool: "calculator", NeedText: "[[COMBO-7]]"},
		{Name: "按需读取参考", Prompt: "使用 $playing-maze，读取工具参考，告诉我 runmaze 的移动参数；不用执行迷宫。", NeedSkill: "playing-maze", NeedResource: true},
		{Name: "无关问题不加载", Prompt: "用一句话介绍杭州西湖。", ForbidSkill: true},
	} {
		meter.calls.Store(0)
		meter.bytes.Store(0)
		result := runCase(ctx, store, agent, spec)
		result.ModelCalls = meter.calls.Load()
		result.InputBytes = meter.bytes.Load()
		report.Cases = append(report.Cases, result)
		report.Total++
		if result.Passed {
			report.Passed++
		}
	}
	return report, nil
}

func runCase(ctx context.Context, store *conversation.Memory, agent adk.TypedAgent[*schema.AgenticMessage], spec caseSpec) caseResult {
	fmt.Fprintf(os.Stderr, "run %s: %s\n", spec.Name, spec.Prompt)
	result := caseResult{Name: spec.Name, Need: spec.NeedSkill}
	session, err := store.Create(ctx, "eval-skill")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	started := time.Now()
	run, err := agentruntime.ClaimQueuedRun(ctx, store, agent, "eval-skill", session.ID, spec.Prompt)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer run.Close()
	for {
		event := run.Next()
		if event.Kind == agentruntime.EventToolStarted && event.Tool != "" {
			result.Tools = append(result.Tools, event.Tool)
			if event.Tool == "load_skill" {
				result.Loaded = true
				var args struct {
					Name string `json:"name"`
				}
				if json.Unmarshal([]byte(event.Arguments), &args) == nil {
					result.LoadedNames = append(result.LoadedNames, args.Name)
				}
			}
		}
		if !event.IsTerminal() {
			continue
		}
		result.Latency = time.Since(started)
		if event.Kind == agentruntime.EventFailed {
			if event.Err != nil {
				result.Error = event.Err.Error()
			} else {
				result.Error = "agent run failed"
			}
			return result
		}
		result.Answer = event.Text
		loadedNamed := containsTool(result.Tools, "load_skill")
		if spec.ForbidSkill {
			result.Passed = !loadedNamed && strings.TrimSpace(result.Answer) != ""
			return result
		}
		if spec.NeedSkill != "" && !containsTool(result.LoadedNames, spec.NeedSkill) {
			return result
		}
		if spec.NeedTool != "" && !containsTool(result.Tools, spec.NeedTool) {
			return result
		}
		for _, name := range spec.ExtraSkills {
			if !containsTool(result.LoadedNames, name) {
				return result
			}
		}
		if spec.NeedResource && !containsTool(result.Tools, "read_skill_resource") {
			return result
		}
		result.Passed = spec.NeedText == "" || strings.Contains(result.Answer, spec.NeedText)
		return result
	}
}

func containsTool(tools []string, name string) bool {
	for _, item := range tools {
		if item == name {
			return true
		}
	}
	return false
}

func render(report evalReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Skill 目录加载评测\n\n")
	fmt.Fprintf(&b, "- 时间：%s\n", report.Started.Format(time.RFC3339))
	fmt.Fprintf(&b, "- 通过：%d / %d\n\n", report.Passed, report.Total)
	fmt.Fprintf(&b, "Agent 启动时只注册启发式目录。专门 skill 必须通过 `load_skill` 进入上下文。\n\n")
	fmt.Fprintf(&b, "| 用例 | 结果 | 工具 | 模型调用 | 输入字节 | 耗时 |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, item := range report.Cases {
		status := "未通过"
		if item.Passed {
			status = "通过"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %s |\n", item.Name, status, strings.Join(item.Tools, ", "), item.ModelCalls, item.InputBytes, item.Latency.Round(time.Millisecond))
	}
	b.WriteString("\n")
	for _, item := range report.Cases {
		fmt.Fprintf(&b, "## %s\n\n", item.Name)
		fmt.Fprintf(&b, "- 结果：%s\n", map[bool]string{true: "通过", false: "未通过"}[item.Passed])
		fmt.Fprintf(&b, "- load_skill：%v\n", item.Loaded)
		if item.Error != "" {
			fmt.Fprintf(&b, "- 错误：`%s`\n", item.Error)
		}
		if strings.TrimSpace(item.Answer) != "" {
			fmt.Fprintf(&b, "\n```text\n%s\n```\n\n", item.Answer)
		}
	}
	fmt.Fprintf(&b, "## 注册的目录启发式\n\n```markdown\n%s\n```\n", report.Catalog)
	return b.String()
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

// InputBytes measures serialized model messages; it is not billed token usage.
type meterModel struct {
	model.AgenticModel
	calls atomic.Int64
	bytes atomic.Int64
}

func (m *meterModel) record(messages []*schema.AgenticMessage) {
	data, _ := json.Marshal(messages)
	m.calls.Add(1)
	m.bytes.Add(int64(len(data)))
}
func (m *meterModel) Generate(ctx context.Context, in []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	m.record(in)
	return m.AgenticModel.Generate(ctx, in, opts...)
}
func (m *meterModel) Stream(ctx context.Context, in []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	m.record(in)
	return m.AgenticModel.Stream(ctx, in, opts...)
}
func compareReport(path string, current evalReport) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "\nBaseline unavailable: " + err.Error() + "\n"
	}
	var previous evalReport
	if err = json.Unmarshal(data, &previous); err != nil {
		return "\nBaseline could not be decoded: " + err.Error() + "\n"
	}
	var b strings.Builder
	b.WriteString("\n## 同名用例对比\n\n输入字节是序列化消息大小，不是计费 token。\n\n| 用例 | 通过 before → after | 工具调用 before → after | 输入字节 before → after |\n| --- | --- | --- | --- |\n")
	for _, now := range current.Cases {
		for _, old := range previous.Cases {
			if old.Name == now.Name {
				fmt.Fprintf(&b, "| %s | %v → %v | %d → %d | %d → %d |\n", now.Name, old.Passed, now.Passed, len(old.Tools), len(now.Tools), old.InputBytes, now.InputBytes)
			}
		}
	}
	return b.String()
}
