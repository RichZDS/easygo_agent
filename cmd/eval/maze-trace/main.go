// Command maze-trace runs the maze agent on a restated user prompt and writes
// the captured thinking chain to HTML. It does not add maze-design instructions.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
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

	"github.com/cloudwego/eino/components/tool"
)

// restatedPrompt is the user's request, restated and nothing more.
const restatedPrompt = "做一个大一点的迷宫20*20的，然后给我做一个爱心形状的迷宫，终点在内部。"

func main() { os.Exit(run()) }

func run() int {
	configPath := flag.String("config", "configs/eval/memory.yaml", "YAML configuration path")
	outPath := flag.String("out", "doc/eval/maze-think-chain.html", "HTML thinking-chain path")
	promptText := flag.String("prompt", restatedPrompt, "user prompt passed to the agent")
	flag.Parse()
	if err := loadDotEnv(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "maze-trace: %v\n", err)
		return 1
	}
	if _, err := logger.New(logger.DefaultPath()); err != nil {
		fmt.Fprintf(os.Stderr, "maze-trace: %v\n", err)
		return 1
	}
	defer func() { _ = logger.Sync() }()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	page, err := trace(ctx, *configPath, *promptText)
	if err != nil {
		fmt.Fprintf(os.Stderr, "maze-trace: %v\n", err)
		return 1
	}
	body, err := render(page)
	if err != nil {
		fmt.Fprintf(os.Stderr, "maze-trace: %v\n", err)
		return 1
	}
	if err = os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "maze-trace: %v\n", err)
		return 1
	}
	if err = os.WriteFile(*outPath, body, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "maze-trace: %v\n", err)
		return 1
	}
	fmt.Printf("wrote %s (%d events, %d mazes, latency %s)\n", *outPath, len(page.Events), len(page.Mazes), page.Latency)
	return 0
}

type pageData struct {
	Started  time.Time
	Latency  string
	Model    string
	Prompt   string
	Answer   string
	Error    string
	Tools    []string
	Events   []viewEvent
	Mazes    []viewMaze
	Native   []string
	MaxSteps int
}

type viewEvent struct {
	Kind       string
	Label      string
	Text       string
	Tool       string
	CallID     string
	Arguments  string
	Result     string
	HiddenName string
	Maze       *viewMaze
}

type viewMaze struct {
	ID       string
	Name     string
	Describe string
	Rows     [][]string
	Width    int
	Height   int
}

func trace(ctx context.Context, configPath, userPrompt string) (pageData, error) {
	cfg, err := config.Load(configPath, os.LookupEnv)
	if err != nil {
		return pageData{}, err
	}
	// Enough iterations for two maze creations and ordinary tool retries.
	// This is not maze-design guidance.
	if cfg.Agent.MaxSteps < 24 {
		cfg.Agent.MaxSteps = 24
	}
	if cfg.Agent.ContextTokens < 24000 {
		cfg.Agent.ContextTokens = 24000
	}
	if cfg.Model.Timeout < 3*time.Minute {
		cfg.Model.Timeout = 3 * time.Minute
	}
	lib, err := skill.Open(skill.DefaultRoot)
	if err != nil {
		return pageData{}, err
	}
	store := maze.NewStore(maze.DefaultDir)
	before, err := mazeIDs(store)
	if err != nil {
		return pageData{}, err
	}
	agentTools, err := tools.NewAgentTool().WithSkills(lib).WithHiddenMaze(store).AllTools(ctx)
	if err != nil {
		return pageData{}, err
	}
	mainModel, err := chatmodel.New(ctx, cfg.Model)
	if err != nil {
		return pageData{}, err
	}
	summaryModel, err := chatmodel.New(ctx, cfg.SubAgent)
	if err != nil {
		return pageData{}, err
	}
	agent, err := deepagent.New(ctx, deepagent.Config{
		ChatModel:    mainModel,
		SummaryModel: summaryModel,
		Tools:        agentTools,
		Agent:        cfg.Agent,
		Instruction:  prompt.WithSkillCatalog(prompt.SystemPrompt, lib.CatalogPrompt()),
	})
	if err != nil {
		return pageData{}, err
	}
	memory := conversation.NewMemory()
	session, err := memory.Create(ctx, "maze-trace")
	if err != nil {
		return pageData{}, err
	}
	page := pageData{
		Started:  time.Now().UTC(),
		Model:    cfg.Model.Name,
		Prompt:   userPrompt,
		Native:   toolNames(ctx, agentTools),
		MaxSteps: cfg.Agent.MaxSteps,
	}
	fmt.Fprintf(os.Stderr, "maze-trace prompt: %s\n", userPrompt)
	started := time.Now()
	run, err := agentruntime.NewStored(ctx, agent, memory, "maze-trace", session.ID).Start(userPrompt)
	if err != nil {
		return pageData{}, err
	}
	defer run.Close()
	var reasoning strings.Builder
	var speech strings.Builder
	flushReasoning := func() {
		text := strings.TrimSpace(reasoning.String())
		if text == "" {
			return
		}
		page.Events = append(page.Events, viewEvent{Kind: "reasoning", Label: "推理", Text: text})
		reasoning.Reset()
	}
	flushSpeech := func() {
		text := strings.TrimSpace(speech.String())
		if text == "" {
			return
		}
		page.Events = append(page.Events, viewEvent{Kind: "text", Label: "回复草稿", Text: text})
		speech.Reset()
	}
	for {
		event := run.Next()
		switch event.Kind {
		case agentruntime.EventReasoningDelta:
			flushSpeech()
			reasoning.WriteString(event.Text)
		case agentruntime.EventTextDelta:
			flushReasoning()
			speech.WriteString(event.Text)
		case agentruntime.EventToolStarted:
			flushReasoning()
			flushSpeech()
			page.Tools = append(page.Tools, event.Tool)
			item := viewEvent{
				Kind:       "tool_started",
				Label:      "调用工具",
				Tool:       event.Tool,
				CallID:     event.CallID,
				Arguments:  prettyJSON(event.Arguments),
				HiddenName: hiddenToolName(event.Arguments),
				Maze:       extractMaze(event.Arguments),
			}
			page.Events = append(page.Events, item)
		case agentruntime.EventToolFinished:
			flushReasoning()
			flushSpeech()
			item := viewEvent{
				Kind:      "tool_finished",
				Label:     "工具结果",
				Tool:      event.Tool,
				CallID:    event.CallID,
				Result:    prettyJSON(event.Result),
				Arguments: prettyJSON(event.Arguments),
				Maze:      extractMaze(event.Result),
			}
			if item.Maze == nil {
				item.Maze = extractMaze(event.Arguments)
			}
			page.Events = append(page.Events, item)
		case agentruntime.EventCompressing:
			flushReasoning()
			flushSpeech()
			page.Events = append(page.Events, viewEvent{Kind: "compressing", Label: "压缩上下文", Text: "正在压缩历史"})
		case agentruntime.EventCompressed:
			flushReasoning()
			flushSpeech()
			page.Events = append(page.Events, viewEvent{Kind: "compressed", Label: "压缩完成", Text: strings.TrimSpace(event.Text)})
		}
		if !event.IsTerminal() {
			continue
		}
		flushReasoning()
		flushSpeech()
		page.Latency = time.Since(started).Round(time.Millisecond).String()
		if event.Kind == agentruntime.EventFailed {
			if event.Err != nil {
				page.Error = event.Err.Error()
			} else {
				page.Error = "agent run failed"
			}
		}
		page.Answer = event.Text
		if strings.TrimSpace(page.Answer) != "" {
			page.Events = append(page.Events, viewEvent{Kind: "completed", Label: "最终回复", Text: page.Answer})
		}
		break
	}
	after, err := mazeIDs(store)
	if err != nil {
		return pageData{}, err
	}
	for id := range after {
		if before[id] {
			continue
		}
		record, detailErr := store.Detail(id)
		if detailErr != nil {
			continue
		}
		page.Mazes = append(page.Mazes, mazeFromRecord(record))
	}
	return page, nil
}

func mazeIDs(store *maze.Store) (map[string]bool, error) {
	items, err := store.List()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, item := range items {
		out[item.ID] = true
	}
	return out, nil
}

func toolNames(ctx context.Context, items []tool.BaseTool) []string {
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

func prettyJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return raw
	}
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return raw
	}
	return string(out)
}

func hiddenToolName(raw string) string {
	var payload struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return ""
	}
	return payload.Name
}

func extractMaze(raw string) *viewMaze {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return nil
	}
	found := findMaze(value)
	if found == nil {
		return nil
	}
	return found
}

func findMaze(value any) *viewMaze {
	switch typed := value.(type) {
	case map[string]any:
		if grid, ok := asGrid(typed["grid"]); ok {
			item := &viewMaze{
				ID:       asString(typed["id"]),
				Name:     asString(typed["name"]),
				Describe: asString(typed["describe"]),
				Rows:     grid,
				Height:   len(grid),
			}
			if len(grid) > 0 {
				item.Width = len(grid[0])
			}
			return item
		}
		if nested, ok := typed["result"]; ok {
			if found := findMaze(nested); found != nil {
				return found
			}
		}
		if nested, ok := typed["arguments"]; ok {
			if found := findMaze(nested); found != nil {
				return found
			}
		}
		for _, child := range typed {
			if found := findMaze(child); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findMaze(child); found != nil {
				return found
			}
		}
	}
	return nil
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

func asGrid(value any) ([][]string, bool) {
	rows, ok := value.([]any)
	if !ok || len(rows) == 0 {
		return nil, false
	}
	out := make([][]string, 0, len(rows))
	width := -1
	for _, row := range rows {
		cells, ok := row.([]any)
		if !ok {
			return nil, false
		}
		line := make([]string, 0, len(cells))
		for _, cell := range cells {
			text, ok := cell.(string)
			if !ok {
				return nil, false
			}
			line = append(line, text)
		}
		if width < 0 {
			width = len(line)
		} else if len(line) != width {
			return nil, false
		}
		out = append(out, line)
	}
	return out, width > 0
}

func mazeFromRecord(record maze.Record) viewMaze {
	item := viewMaze{
		ID:       record.ID,
		Name:     record.Name,
		Describe: record.Describe,
		Rows:     record.Grid,
		Height:   len(record.Grid),
	}
	if len(record.Grid) > 0 {
		item.Width = len(record.Grid[0])
	}
	return item
}

func render(page pageData) ([]byte, error) {
	tpl, err := template.New("think").Funcs(template.FuncMap{
		"join": strings.Join,
		"cellClass": func(cell string) string {
			switch cell {
			case "墙":
				return "wall"
			case "路":
				return "path"
			case "起":
				return "start"
			case "终":
				return "goal"
			case "人":
				return "man"
			default:
				return "unknown"
			}
		},
	}).Parse(htmlTemplate)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err = tpl.Execute(&buf, page); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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

const htmlTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>迷宫 Agent 思考链</title>
  <style>
    :root {
      --bg: #f4efe6;
      --paper: #fffaf2;
      --ink: #1c1915;
      --muted: #6b645b;
      --line: #e3d8c6;
      --user: #24364a;
      --user-bg: #e8eef5;
      --think: #5b4636;
      --think-bg: #f3eadc;
      --tool: #1f4d63;
      --tool-bg: #e7f3f7;
      --result: #1f6b45;
      --result-bg: #e5f4ea;
      --fail: #9b2c2c;
      --fail-bg: #fdecec;
      --accent: #c45c26;
      --wall: #2b2723;
      --path: #f7f1e6;
      --start: #2f9e62;
      --goal: #d4534a;
      --man: #e0b33a;
    }
    * { box-sizing: border-box; }
    html, body { margin: 0; padding: 0; background: var(--bg); color: var(--ink); }
    body {
      font: 16px/1.65 "Iowan Old Style", "Palatino Linotype", "Songti SC", "Source Han Serif SC", serif;
    }
    .wrap { max-width: 1080px; margin: 0 auto; padding: 48px 24px 80px; }
    header.hero {
      border-bottom: 3px solid var(--ink);
      padding-bottom: 28px;
      margin-bottom: 36px;
    }
    .kicker {
      letter-spacing: .18em;
      text-transform: uppercase;
      font-size: 12px;
      color: var(--accent);
      font-family: "Segoe UI", sans-serif;
      margin: 0 0 10px;
    }
    h1 { font-size: 40px; line-height: 1.15; margin: 0 0 12px; font-weight: 700; }
    .lede { font-size: 18px; color: var(--muted); margin: 0; max-width: 46em; }
    .meta {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      gap: 12px;
      margin-top: 28px;
    }
    .stat {
      background: var(--paper);
      border: 1px solid var(--line);
      padding: 14px 16px;
    }
    .stat b { display: block; font-size: 18px; word-break: break-all; }
    .stat span { color: var(--muted); font-size: 13px; font-family: "Segoe UI", sans-serif; }
    h2 {
      font-size: 28px;
      margin: 48px 0 16px;
      padding-top: 12px;
      border-top: 1px solid var(--line);
    }
    .prompt, .answer, .error, .event {
      background: var(--paper);
      border: 1px solid var(--line);
      padding: 16px 18px;
      margin: 0 0 16px;
    }
    .prompt { background: var(--user-bg); border-color: #c9d6e4; }
    .event.reasoning { background: var(--think-bg); }
    .event.tool_started { background: var(--tool-bg); }
    .event.tool_finished { background: var(--result-bg); }
    .event.completed { background: #f6f0e4; }
    .event.error, .error { background: var(--fail-bg); border-color: #f0c2c2; }
    .label {
      font-family: "Segoe UI", sans-serif;
      font-size: 12px;
      letter-spacing: .08em;
      text-transform: uppercase;
      color: var(--muted);
      margin: 0 0 8px;
    }
    pre {
      white-space: pre-wrap;
      word-break: break-word;
      font: 13px/1.5 ui-monospace, "Cascadia Code", Consolas, monospace;
      margin: 0;
    }
    details { margin-top: 10px; }
    summary { cursor: pointer; color: var(--muted); font-family: "Segoe UI", sans-serif; font-size: 13px; }
    .maze {
      display: grid;
      gap: 1px;
      background: #cfc4b3;
      width: max-content;
      max-width: 100%;
      overflow: auto;
      margin: 12px 0 4px;
      padding: 1px;
    }
    .cell {
      width: 18px;
      height: 18px;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 11px;
      font-family: "Segoe UI", sans-serif;
    }
    .cell.wall { background: var(--wall); color: #f4efe6; }
    .cell.path { background: var(--path); color: #8a8175; }
    .cell.start { background: var(--start); color: white; }
    .cell.goal { background: var(--goal); color: white; }
    .cell.man { background: var(--man); color: #2b2723; }
    .cell.unknown { background: #d9c7ae; }
    .legend { color: var(--muted); font-size: 13px; font-family: "Segoe UI", sans-serif; }
    .gallery { display: grid; gap: 18px; }
    @media (max-width: 800px) {
      .meta { grid-template-columns: 1fr 1fr; }
      h1 { font-size: 30px; }
      .cell { width: 14px; height: 14px; font-size: 9px; }
    }
  </style>
</head>
<body>
  <div class="wrap">
    <header class="hero">
      <p class="kicker">EasyGo Maze Agent</p>
      <h1>迷宫 Agent 思考链</h1>
      <p class="lede">只复述用户原话，不附加迷宫设计指导。下面是 agent 当时的推理、工具调用、工具结果和最终回复。</p>
      <div class="meta">
        <div class="stat"><b>{{.Started.Format "2006-01-02 15:04:05 MST"}}</b><span>开始时间</span></div>
        <div class="stat"><b>{{.Model}}</b><span>模型</span></div>
        <div class="stat"><b>{{.Latency}}</b><span>耗时</span></div>
        <div class="stat"><b>{{len .Events}}</b><span>事件数</span></div>
      </div>
    </header>

    <h2>交给 agent 的话</h2>
    <div class="prompt">
      <p class="label">复述，无额外指导</p>
      <pre>{{.Prompt}}</pre>
    </div>
    <p class="legend">原生工具：{{join .Native ", "}}　·　步数上限：{{.MaxSteps}}　·　实际调用：{{join .Tools ", "}}</p>

    {{if .Error}}
    <div class="error">
      <p class="label">运行错误</p>
      <pre>{{.Error}}</pre>
    </div>
    {{end}}

    <h2>思考链</h2>
    {{range .Events}}
    <article class="event {{.Kind}}">
      <p class="label">{{.Label}}{{if .Tool}} · {{.Tool}}{{if .HiddenName}} / {{.HiddenName}}{{end}}{{end}}</p>
      {{if .Text}}<pre>{{.Text}}</pre>{{end}}
      {{if .Maze}}
      <p class="legend">{{if .Maze.Name}}{{.Maze.Name}} · {{end}}{{.Maze.Width}} × {{.Maze.Height}}{{if .Maze.Describe}} · {{.Maze.Describe}}{{end}}{{if .Maze.ID}} · {{.Maze.ID}}{{end}}</p>
      <div class="maze" style="grid-template-columns: repeat({{.Maze.Width}}, 18px);">
        {{range .Maze.Rows}}{{range .}}<div class="cell {{cellClass .}}" title="{{.}}">{{.}}</div>{{end}}{{end}}
      </div>
      {{end}}
      {{if .Arguments}}
      <details><summary>工具参数</summary><pre>{{.Arguments}}</pre></details>
      {{end}}
      {{if .Result}}
      <details><summary>工具返回</summary><pre>{{.Result}}</pre></details>
      {{end}}
    </article>
    {{end}}

    <h2>本轮新建的迷宫</h2>
    {{if .Mazes}}
    <div class="gallery">
      {{range .Mazes}}
      <article class="event completed">
        <p class="label">{{.Name}} · {{.Width}} × {{.Height}}</p>
        <p class="legend">{{.Describe}}{{if .ID}} · {{.ID}}{{end}}</p>
        <div class="maze" style="grid-template-columns: repeat({{.Width}}, 18px);">
          {{range .Rows}}{{range .}}<div class="cell {{cellClass .}}" title="{{.}}">{{.}}</div>{{end}}{{end}}
        </div>
      </article>
      {{end}}
    </div>
    {{else}}
    <p class="legend">这一轮没有成功写入新的迷宫文件。</p>
    {{end}}
  </div>
</body>
</html>
`
