// Command maze-eval compares bound maze tools versus catalog + load_skill + call_tool.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"go.uber.org/zap"
	"os"
	"path/filepath"
	"strings"
	"time"

	"easygo-agent/internal/agent/chatmodel"
	"easygo-agent/internal/agent/deepagent"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/logger"
	"easygo-agent/internal/maze"
	"easygo-agent/internal/prompt"
	"easygo-agent/internal/skill"
	"easygo-agent/internal/tools"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func main() { os.Exit(run()) }

func run() int {
	configPath := flag.String("config", "configs/eval/memory.yaml", "YAML configuration path")
	outPath := flag.String("out", "doc/eval/maze.md", "markdown report path")
	flag.Parse()
	if err := loadDotEnv(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "maze-eval: %v\n", err)
		return 1
	}
	if _, err := logger.New(logger.DefaultPath(), zap.DebugLevel); err != nil {
		fmt.Fprintf(os.Stderr, "maze-eval: %v\n", err)
		return 1
	}
	defer func() { _ = logger.Sync() }()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	report, err := evaluate(ctx, *configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "maze-eval: %v\n", err)
		return 1
	}
	if err = os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "maze-eval: %v\n", err)
		return 1
	}
	if err = os.WriteFile(*outPath, []byte(render(report)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "maze-eval: %v\n", err)
		return 1
	}
	fmt.Printf("wrote %s (%d/%d cases passed)\n", *outPath, report.Passed, report.Total)
	return 0
}

type mode struct {
	Name      string
	Agent     adk.TypedAgent[*schema.AgenticMessage]
	Native    []string
	Heuristic bool
}

type caseSpec struct {
	Mode     mode
	Name     string
	Prompt   string
	Need     []string
	NeedTool string
	Load     bool
	NoLoad   bool
}

type caseResult struct {
	Name    string
	Mode    string
	Passed  bool
	Tools   []string
	Native  []string
	Answer  string
	Error   string
	Latency time.Duration
}

type evalReport struct {
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
	if cfg.Agent.MaxSteps < 8 {
		cfg.Agent.MaxSteps = 8
	}
	lib, err := skill.Open(skill.DefaultRoot)
	if err != nil {
		return evalReport{}, err
	}
	store := maze.NewStore(maze.DefaultDir)
	heuristicTools, err := tools.NewAgentTool().WithSkills(lib).WithHiddenMaze(store).AllTools(ctx)
	if err != nil {
		return evalReport{}, err
	}
	boundTools, err := tools.NewAgentTool().WithBoundMaze(store).AllTools(ctx)
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
	heuristicAgent, err := deepagent.New(ctx, deepagent.Config{
		ChatModel: mainModel, SummaryModel: summaryModel, Tools: heuristicTools, Agent: cfg.Agent,
		Instruction: prompt.WithSkillCatalog(prompt.SystemPrompt, lib.CatalogPrompt()),
	})
	if err != nil {
		return evalReport{}, err
	}
	boundAgent, err := deepagent.New(ctx, deepagent.Config{
		ChatModel: mainModel, SummaryModel: summaryModel, Tools: boundTools, Agent: cfg.Agent,
		Instruction: prompt.SystemPrompt,
	})
	if err != nil {
		return evalReport{}, err
	}
	heuristic := mode{Name: "启发式", Agent: heuristicAgent, Native: names(ctx, heuristicTools), Heuristic: true}
	bound := mode{Name: "启动绑定", Agent: boundAgent, Native: names(ctx, boundTools)}
	memory := conversation.NewMemory()
	report := evalReport{Started: time.Now().UTC()}
	for _, spec := range []caseSpec{
		{Mode: heuristic, Name: "启发式列出迷宫", Prompt: "列出目前存在的所有迷宫，带上名称。", Need: []string{"小径"}, NeedTool: "call_tool", Load: true},
		{Mode: heuristic, Name: "启发式跑小径", Prompt: "请用指令 2 4 跑迷宫「小径」（id sample-xiaojing），告诉我人能不能到终点。", Need: []string{"终"}, NeedTool: "call_tool", Load: true},
		{Mode: bound, Name: "启动绑定列出迷宫", Prompt: "列出目前存在的所有迷宫，带上名称。", Need: []string{"小径"}, NeedTool: "listmaze", NoLoad: true},
		{Mode: bound, Name: "启动绑定跑小径", Prompt: "请用指令 2 4 跑迷宫 sample-xiaojing，告诉我能不能到终点。", Need: []string{"终"}, NeedTool: "runmaze", NoLoad: true},
	} {
		result := runCase(ctx, memory, spec)
		report.Cases = append(report.Cases, result)
		report.Total++
		if result.Passed {
			report.Passed++
		}
	}
	return report, nil
}

func runCase(ctx context.Context, store *conversation.Memory, spec caseSpec) caseResult {
	fmt.Fprintf(os.Stderr, "run %s/%s: %s\n", spec.Mode.Name, spec.Name, spec.Prompt)
	result := caseResult{Name: spec.Name, Mode: spec.Mode.Name, Native: spec.Mode.Native}
	session, err := store.Create(ctx, "eval-maze")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	started := time.Now()
	run, err := agentruntime.ClaimQueuedRun(ctx, store, spec.Mode.Agent, "eval-maze", session.ID, spec.Prompt)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer run.Close()
	for {
		event := run.Next()
		if event.Kind == agentruntime.EventToolStarted && event.Tool != "" {
			result.Tools = append(result.Tools, event.Tool)
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
		loaded := contains(result.Tools, "load_skill")
		if spec.Load && !loaded {
			return result
		}
		if spec.NoLoad && loaded {
			return result
		}
		if spec.NeedTool != "" && !contains(result.Tools, spec.NeedTool) {
			return result
		}
		folded := result.Answer
		for _, need := range spec.Need {
			if !strings.Contains(folded, need) {
				return result
			}
		}
		result.Passed = true
		return result
	}
}

func names(ctx context.Context, items []tool.BaseTool) []string {
	var out []string
	for _, item := range items {
		info, err := item.Info(ctx)
		if err != nil {
			continue
		}
		out = append(out, info.Name)
	}
	return out
}

func contains(items []string, name string) bool {
	for _, item := range items {
		if item == name {
			return true
		}
	}
	return false
}

func render(report evalReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 迷宫工具绑定 vs 启发式加载\n\n")
	fmt.Fprintf(&b, "- 时间：%s\n", report.Started.Format(time.RFC3339))
	fmt.Fprintf(&b, "- 通过：%d / %d\n\n", report.Passed, report.Total)
	b.WriteString("启发式模式：系统提示词只有 skill 目录；迷宫工具不在原生 tool 列表，必须 `load_skill(playing-maze)` 再 `call_tool`。\n\n")
	b.WriteString("启动绑定模式：`listmaze` / `detailmaze` / `runmaze` / `makemaze` 直接出现在模型可见的 tool schema 里，系统提示词没有迷宫 skill。\n\n")
	fmt.Fprintf(&b, "| 模式 | 用例 | 结果 | 调用的工具 | 耗时 |\n| --- | --- | --- | --- | --- |\n")
	for _, item := range report.Cases {
		status := "未通过"
		if item.Passed {
			status = "通过"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", item.Mode, item.Name, status, strings.Join(item.Tools, ", "), item.Latency.Round(time.Millisecond))
	}
	b.WriteString("\n")
	for _, item := range report.Cases {
		fmt.Fprintf(&b, "## %s · %s\n\n", item.Mode, item.Name)
		fmt.Fprintf(&b, "- 结果：%s\n", map[bool]string{true: "通过", false: "未通过"}[item.Passed])
		fmt.Fprintf(&b, "- 原生 tool 列表：`%s`\n", strings.Join(item.Native, "`, `"))
		fmt.Fprintf(&b, "- 实际调用：`%s`\n", strings.Join(item.Tools, "`, `"))
		if item.Error != "" {
			fmt.Fprintf(&b, "- 错误：`%s`\n", item.Error)
		}
		if strings.TrimSpace(item.Answer) != "" {
			fmt.Fprintf(&b, "\n```text\n%s\n```\n\n", item.Answer)
		}
	}
	b.WriteString("## 结论要点\n\n")
	b.WriteString("- 启动绑定：模型从 tool schema 就知道迷宫工具，不必 load_skill。后续工具一多，schema 会占满上下文。\n")
	b.WriteString("- 启发式：模型先只看见目录和 `call_tool`；迷宫细节在 playing-maze skill 加载之后才进入本轮。\n")
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
