package workshop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const defaultOutputLimit = 4 * 1024 * 1024
const diagnosticLimit = 16 * 1024

type CommandRunner struct {
	engines   map[string]EngineConfig
	gateway   *ModelGateway
	maxOutput int
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func NewCommandRunner(engines map[string]EngineConfig, maxOutput int) (*CommandRunner, error) {
	if maxOutput == 0 {
		maxOutput = defaultOutputLimit
	}
	if maxOutput < 1024 || maxOutput > 64*1024*1024 {
		return nil, fmt.Errorf("%w: max_output_bytes must be 1024..67108864", ErrInvalid)
	}
	r := &CommandRunner{engines: map[string]EngineConfig{}, maxOutput: maxOutput}
	for name, engine := range engines {
		if !knownEngine(name) {
			return nil, fmt.Errorf("%w: unknown engine", ErrInvalid)
		}
		if engine.Binary == "" {
			engine.Binary = name
		}
		path, err := exec.LookPath(engine.Binary)
		if err != nil {
			return nil, fmt.Errorf("engine %s executable unavailable", name)
		}
		engine.Binary, err = filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		for _, name := range engine.EnvAllowlist {
			if !envName.MatchString(name) || reservedEnvironment(name) {
				return nil, fmt.Errorf("%w: environment name", ErrInvalid)
			}
		}
		engine.EnvAllowlist = append([]string(nil), engine.EnvAllowlist...)
		r.engines[name] = engine
	}
	return r, nil
}

func engineArgs(in Invocation) ([]string, error) {
	if err := validateWorkflow(in.Workflow); err != nil {
		return nil, err
	}
	if in.SessionID != "" {
		if _, err := uuid.Parse(in.SessionID); err != nil {
			return nil, errors.New("invalid native session id")
		}
	}
	w := in.Workflow
	switch w.Engine {
	case "codex":
		args := []string{"exec"}
		if in.SessionID != "" {
			args = append(args, "resume")
		}
		// resume does not accept --sandbox. Config overrides are supported by both.
		args = append(args, "--json", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules", "-c", `approval_policy="never"`, "-c", `sandbox_mode="`+w.Policy+`"`, "--model", w.Model)
		if in.SessionID != "" {
			args = append(args, in.SessionID)
		}
		return append(args, "-"), nil
	case "pi":
		tools := "read,grep,find,ls"
		if w.Policy == "workspace-write" {
			tools += ",edit,write"
		}
		return []string{"--print", "--mode", "json", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files", "--no-approve", "--tools", tools, "--model", w.Model}, nil
	case "openclaw":
		return []string{"agent", "--local", "--json", "--agent", "main", "--timeout", fmt.Sprint(w.TimeoutSeconds)}, nil
	case "claude":
		tools, mode := "Read,Glob,Grep", "dontAsk"
		if w.Policy == "workspace-write" {
			tools, mode = "Read,Glob,Grep,Edit,Write", "acceptEdits"
		}
		// Restricted file tools are confined to cwd; there are no shell or MCP tools.
		args := []string{"--print", "--output-format", "stream-json", "--verbose", "--bare", "--restricted", "--strict-mcp-config", "--permission-prompts", "none", "--permission-mode", mode, "--tools", tools, "--model", w.Model}
		if in.SessionID != "" {
			args = append(args, "--resume", in.SessionID)
		}
		return args, nil
	}
	return nil, errors.New("unsupported engine")
}

func (r *CommandRunner) Run(ctx context.Context, in Invocation, emit func(Event) error) (Result, error) {
	engine, ok := r.engines[in.Workflow.Engine]
	if !ok {
		return Result{}, errors.New("engine not configured")
	}
	args, err := engineArgs(in)
	if err != nil {
		return Result{}, err
	}
	home := filepath.Join(in.Workspace, ".workshop-home")
	if info, e := os.Lstat(home); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return Result{}, errors.New("task home must not be a symlink")
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return Result{}, err
	}
	env := map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": home, "CODEX_HOME": filepath.Join(home, "codex"), "CLAUDE_CONFIG_DIR": filepath.Join(home, "claude")}
	for _, dir := range []string{env["CODEX_HOME"], env["CLAUDE_CONFIG_DIR"]} {
		if e := os.MkdirAll(dir, 0700); e != nil {
			return Result{}, e
		}
	}
	var secrets []string
	for _, name := range engine.EnvAllowlist {
		if in.Workflow.RuntimeSpec != nil {
			continue
		} // profiles inject only their selected credential
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
			if value != "" && name != "PATH" && name != "HOME" && name != "TMPDIR" && name != "CODEX_HOME" && name != "CLAUDE_CONFIG_DIR" {
				secrets = append(secrets, value)
			}
		}
	}
	args, runtimeSecrets, cleanup, err := r.configureRuntime(ctx, in, args, env)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	secrets = append(secrets, runtimeSecrets...)
	return runNative(ctx, in.Workflow.Engine, r.maxOutput, secrets, emit, func(childCtx context.Context, stdout, stderr io.Writer) error {
		cmd := exec.CommandContext(childCtx, engine.Binary, args...)
		cmd.Dir = in.Workspace
		for name, value := range env {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
		sort.Strings(cmd.Env)
		cmd.Stdin = strings.NewReader(in.Workflow.Instructions + "\n\nUser input:\n" + in.Input)
		if err := configureProcess(cmd); err != nil {
			return err
		}
		cmd.WaitDelay = time.Second
		cmd.Stdout, cmd.Stderr = stdout, stderr
		err := cmd.Run()
		killProcessGroup(cmd)
		return err
	})
}

// runNative keeps parsing, output limits, redaction and terminal/session checks
// identical across explicit host and mandatory Docker execution.
func runNative(ctx context.Context, engine string, maxOutput int, secrets []string, emit func(Event) error, execute func(context.Context, io.Writer, io.Writer) error) (Result, error) {
	redact := redactor(secrets)
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	parser := &streamParser{engine: engine, emit: emit, redact: redact, limit: maxOutput, cancel: cancel}
	diagnostics := &boundedBuffer{limit: diagnosticLimit + maxSecretLength(secrets)}
	err := execute(childCtx, parser, diagnostics)
	parseErr := parser.finish()
	result := parser.result
	text := redact(diagnostics.buf.String())
	truncated := diagnostics.overflow || len(text) > diagnosticLimit
	if len(text) > diagnosticLimit {
		text = text[:diagnosticLimit]
	}
	if truncated {
		text += " [diagnostics truncated]"
	}
	if text != "" {
		if emitErr := emit(Event{Kind: "diagnostic", Text: text}); emitErr != nil {
			return result, emitErr
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if parseErr != nil {
		return result, parseErr
	}
	if err != nil {
		return result, errors.New("engine process failed (see bounded diagnostic events)")
	}
	if !parser.success {
		return result, errors.New("engine exited without terminal success event")
	}
	if result.SessionID == "" {
		return result, errors.New("engine succeeded without native session id")
	}
	return result, nil
}

type boundedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buf.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.buf.Write(p)
	return n, nil
}
func maxSecretLength(secrets []string) int {
	n := 0
	for _, s := range secrets {
		if len(s) > n {
			n = len(s)
		}
	}
	return n
}
func redactor(secrets []string) func(string) string {
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return func(text string) string {
		for _, secret := range secrets {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
		return text
	}
}

type nativeEvent struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	ThreadID  string `json:"thread_id"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	Usage     struct {
		Input     int64 `json:"input_tokens"`
		Output    int64 `json:"output_tokens"`
		Cached    int64 `json:"cached_input_tokens"`
		CacheRead int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
	Item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Message struct {
		Role       string `json:"role"`
		StopReason string `json:"stopReason"`
		Usage      struct {
			Input     int64 `json:"input"`
			Output    int64 `json:"output"`
			CacheRead int64 `json:"cacheRead"`
		} `json:"usage"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

type streamParser struct {
	engine       string
	emit         func(Event) error
	redact       func(string) string
	limit, total int
	pending      []byte
	result       Result
	success      bool
	piReady      bool
	err          error
	cancel       context.CancelFunc
}

func (p *streamParser) fail(err error) {
	if p.err == nil {
		p.err = err
		p.cancel()
	}
}
func (p *streamParser) Write(data []byte) (int, error) {
	n := len(data)
	if p.err != nil {
		return n, nil
	}
	p.total += n
	if p.total > p.limit {
		p.fail(errors.New("engine output limit exceeded"))
		return n, nil
	}
	p.pending = append(p.pending, data...)
	if p.engine == "openclaw" {
		return n, nil
	}
	for {
		index := bytes.IndexByte(p.pending, '\n')
		if index < 0 {
			break
		}
		line := p.pending[:index]
		if len(bytes.TrimSpace(line)) > 0 {
			p.parse(line)
		}
		p.pending = p.pending[index+1:]
		if p.err != nil {
			break
		}
	}
	return n, nil
}
func (p *streamParser) finish() error {
	if p.engine == "openclaw" {
		if p.err == nil {
			p.parseOpenClaw(p.pending)
		}
		return p.err
	}
	if p.err == nil && len(bytes.TrimSpace(p.pending)) > 0 {
		p.parse(p.pending)
	}
	return p.err
}
func (p *streamParser) send(e Event) {
	e.Text = p.redact(e.Text)
	if err := p.emit(e); err != nil {
		p.fail(fmt.Errorf("persist engine event: %w", err))
	}
}
func (p *streamParser) parse(line []byte) {
	if p.engine == "pi" {
		p.parsePiRaw(line)
		return
	}
	var event nativeEvent
	if json.Unmarshal(line, &event) != nil || event.Type == "" {
		p.fail(errors.New("malformed engine JSONL event"))
		return
	}
	if p.engine == "pi" {
		p.parsePi(event)
		return
	}
	id := event.SessionID
	if p.engine == "codex" {
		id = event.ThreadID
	}
	if id != "" && id != p.result.SessionID {
		if _, err := uuid.Parse(id); err != nil {
			p.fail(errors.New("invalid native session id"))
			return
		}
		p.result.SessionID = id
		p.send(Event{Kind: "session", SessionID: id})
	}
	if p.err != nil {
		return
	}
	text := ""
	if p.engine == "claude" {
		switch event.Type {
		case "assistant":
			for _, content := range event.Message.Content {
				if content.Type == "text" {
					text += content.Text
				}
			}
		case "result":
			if event.IsError || event.Subtype != "success" {
				p.fail(errors.New("engine reported terminal failure"))
				return
			}
			if p.success {
				p.fail(errors.New("duplicate engine terminal event"))
				return
			}
			p.success = true
			p.result.Text = p.redact(event.Result)
			p.result.Usage = Usage{InputTokens: event.Usage.Input, OutputTokens: event.Usage.Output, CachedInputTokens: event.Usage.CacheRead}
			p.send(Event{Kind: "result", Text: event.Result, Usage: &p.result.Usage})
		}
	} else {
		switch event.Type {
		case "turn.failed", "error":
			p.fail(errors.New("engine reported failure"))
			return
		case "item.completed":
			if event.Item.Type == "agent_message" {
				text = event.Item.Text
				p.result.Text += p.redact(text)
			}
		case "turn.completed":
			if p.success {
				p.fail(errors.New("duplicate engine terminal event"))
				return
			}
			p.success = true
			p.result.Usage = Usage{InputTokens: event.Usage.Input, OutputTokens: event.Usage.Output, CachedInputTokens: event.Usage.Cached}
			p.send(Event{Kind: "result", Usage: &p.result.Usage})
		}
	}
	if text != "" {
		p.send(Event{Kind: "text", Text: text})
	}
}
