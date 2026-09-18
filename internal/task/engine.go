package task

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"easygo-agent/internal/toolregistry"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type Role struct {
	Instruction string   `yaml:"instruction" json:"instruction"`
	Tools       []string `yaml:"tools" json:"tools"`
}
type Config struct {
	Enabled      bool
	Workers      int
	MaxSteps     int
	Timeout      time.Duration
	LeaseTTL     time.Duration
	PollInterval time.Duration
	Roles        map[string]Role
}

func DefaultConfig() Config {
	return Config{Workers: 2, MaxSteps: 32, Timeout: 15 * time.Minute, LeaseTTL: 30 * time.Second, PollInterval: 250 * time.Millisecond, Roles: map[string]Role{
		"analyst":  {Instruction: "Analyze the supplied materials and return evidence with limitations. Use read-only tools.", Tools: []string{"calculator", "list_skills", "load_skill", "read_skill_resource", "listmaze", "detailmaze", "runmaze"}},
		"executor": {Instruction: "Execute the brief using the explicitly allowed business tools. Verify each acceptance criterion.", Tools: []string{"calculator", "list_skills", "load_skill", "read_skill_resource", "listmaze", "detailmaze", "runmaze", "makemaze"}},
	}}
}

const workerPrompt = `You are an isolated background agent. Work only on the supplied brief and your own execution history.
Respect all constraints and acceptance criteria. You cannot delegate or access the main conversation or sandbox.
Use report_progress to record a plan and progress. Use finish_task with a summary, concrete evidence and any unmet criteria to finish. Completion requires evidence; include uncertainty in unmet.
Load explicitly requested $skills and combine relevant skills. Use list_skills, load_skill and read_skill_resource only if registered.
Tool errors are evidence of failure. Do not claim an action succeeded without a successful tool result.
Call finish_task after the work and tool results have been verified.`

type Engine struct {
	Store        Store
	Model        model.AgenticModel
	Registry     *toolregistry.Registry
	Config       Config
	SkillVersion string
}

func (e *Engine) role(ctx context.Context, name string) (*toolregistry.Registry, string, error) {
	role, ok := e.Config.Roles[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown role %q", name)
	}
	r, err := e.Registry.Subset(ctx, role.Tools, name == "analyst")
	if err != nil {
		return nil, "", err
	}
	progress, err := utils.InferTool("report_progress", "Persist progress and the current plan.", func(ctx context.Context, in struct {
		Progress string     `json:"progress"`
		Plan     []PlanStep `json:"plan"`
	}) (any, error) {
		return in, nil
	})
	if err != nil {
		return nil, "", err
	}
	finish, err := utils.InferTool("finish_task", "Finish with verified evidence and explicit unmet acceptance criteria.", func(ctx context.Context, in Result) (Result, error) {
		if strings.TrimSpace(in.Summary) == "" {
			return Result{}, fmt.Errorf("summary is required")
		}
		if len(in.Evidence) == 0 && len(in.Unmet) == 0 {
			return Result{}, fmt.Errorf("evidence or unmet criteria are required")
		}
		return in, nil
	})
	if err != nil {
		return nil, "", err
	}
	if err = r.Register(ctx, progress, "task-local", toolregistry.Idempotent, false); err != nil {
		return nil, "", err
	}
	if err = r.Register(ctx, finish, "task-local", toolregistry.Idempotent, false); err != nil {
		return nil, "", err
	}
	version, err := r.Version(ctx)
	if err != nil {
		return nil, "", err
	}
	data, _ := json.Marshal(role)
	version = fmt.Sprintf("%x", sha256.Sum256([]byte(version+e.SkillVersion+workerPrompt+string(data))))
	return r, version, nil
}
func (e *Engine) Validate(ctx context.Context) error {
	if e.Model == nil || e.Store == nil || e.Registry == nil {
		return errors.New("task engine dependencies are required")
	}
	for name := range e.Config.Roles {
		if _, _, err := e.role(ctx, name); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) save(ctx context.Context, t *Task) error {
	for _, call := range t.Checkpoint.Calls {
		found := false
		for i := range t.Calls {
			if t.Calls[i].Key == call.Key {
				t.Calls[i] = call
				found = true
				break
			}
		}
		if !found {
			t.Calls = append(t.Calls, call)
		}
	}
	return e.Store.Save(ctx, *t, t.Token)
}
func (e *Engine) stop(ctx context.Context, t *Task, status Status, reason string) error {
	t.Status = status
	t.Reason = reason
	t.Event(string(status), reason)
	return e.save(ctx, t)
}

// Execute only runs a claimed task. Every model output is durably saved before
// the first call; every call is registered before invoking external code.
func (e *Engine) Execute(ctx context.Context, t Task) error {
	r, version, err := e.role(ctx, t.Brief.Role)
	if err != nil {
		return e.stop(ctx, &t, Blocked, err.Error())
	}
	cp := &t.Checkpoint
	if cp.Format != 0 && (cp.Format != FormatVersion || cp.Version != version) {
		return e.stop(ctx, &t, Blocked, "checkpoint format or prompt/skill/tool configuration changed; restore the recorded configuration before resuming")
	}
	if cp.Format == 0 {
		cp.Format = FormatVersion
		cp.Version = version
		brief, _ := json.Marshal(t.Brief)
		cp.Messages = []*schema.AgenticMessage{schema.SystemAgenticMessage(workerPrompt + "\n" + e.Config.Roles[t.Brief.Role].Instruction), schema.UserAgenticMessage(string(brief))}
	}
	if cp.StepLimit == 0 {
		cp.StepLimit = e.Config.MaxSteps
	}
	if cp.Deadline.IsZero() {
		cp.Deadline = time.Now().UTC().Add(e.Config.Timeout)
	}
	if err = e.save(ctx, &t); err != nil {
		return err
	}
	deadlineCtx, cancel := context.WithDeadline(ctx, cp.Deadline)
	defer cancel()
	infos := []*schema.ToolInfo{}
	for _, name := range r.Names() {
		entry, _ := r.Lookup(name)
		infos = append(infos, entry.Info)
	}
	for {
		if err = deadlineCtx.Err(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e.timeout(ctx, &t)
		}
		if len(cp.Calls) > 0 {
			if err = e.batch(deadlineCtx, &t, r); err != nil {
				if deadlineCtx.Err() != nil && ctx.Err() == nil {
					return e.timeout(ctx, &t)
				}
				return err
			}
			if t.Status != Running {
				return nil
			}
		}
		cp.Continue = false
		if cp.Steps >= cp.StepLimit {
			return e.stop(ctx, &t, Failed, "execution step budget exhausted")
		}
		// Reserve before the model request: repeated process crashes cannot reset budget.
		cp.Steps++
		t.Event("model_started", fmt.Sprintf("step %d", cp.Steps))
		if err = e.save(ctx, &t); err != nil {
			return err
		}
		brief, _ := json.Marshal(t.Brief)
		messages := append([]*schema.AgenticMessage(nil), cp.Messages...)
		messages = append([]*schema.AgenticMessage{schema.SystemAgenticMessage("Current brief and continuation constraints: " + string(brief))}, messages...)
		output, generateErr := e.Model.Generate(deadlineCtx, messages, model.WithTools(infos))
		if generateErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e.stop(ctx, &t, Failed, "model: "+generateErr.Error())
		}
		if output == nil {
			return e.stop(ctx, &t, Failed, "model returned no output")
		}
		cp.Messages = append(cp.Messages, output)
		seen := map[string]bool{}
		for _, message := range cp.Messages[:len(cp.Messages)-1] {
			if message == nil {
				continue
			}
			for _, block := range message.ContentBlocks {
				if block == nil {
					continue
				}
				if block.FunctionToolCall != nil {
					seen[block.FunctionToolCall.CallID] = true
				}
			}
		}
		for _, block := range output.ContentBlocks {
			if block == nil {
				continue
			}
			if call := block.FunctionToolCall; call != nil {
				if call.CallID == "" || seen[call.CallID] {
					return e.stop(ctx, &t, Blocked, "model returned missing or duplicate tool call ID")
				}
				seen[call.CallID] = true
				retry := toolregistry.Unsafe
				if entry, ok := r.Lookup(call.Name); ok {
					retry = entry.Retry
				}
				cp.Calls = append(cp.Calls, Call{ID: call.CallID, Name: call.Name, Arguments: call.Arguments, Key: fmt.Sprintf("%s:%d:%s", t.ID, t.Version, call.CallID), Retry: retry})
			}
		}
		t.Event("model_output", fmt.Sprintf("step %d: %d calls", cp.Steps, len(cp.Calls)))
		if err = e.save(ctx, &t); err != nil {
			return err
		}
		if len(cp.Calls) == 0 {
			cp.Messages = append(cp.Messages, schema.UserAgenticMessage("Submit finish_task with evidence and unmet criteria when ready; otherwise continue the brief."))
			if err = e.save(ctx, &t); err != nil {
				return err
			}
		}
	}
}
func (e *Engine) batch(ctx context.Context, t *Task, r *toolregistry.Registry) error {
	cp := &t.Checkpoint
	for i := range cp.Calls {
		c := &cp.Calls[i]
		if c.Done {
			continue
		}
		if c.Started && c.Retry != toolregistry.ReadOnly && c.Retry != toolregistry.Idempotent && !c.Approved {
			return e.stop(ctx, t, Blocked, fmt.Sprintf("call %s (%s) has an uncertain write result; explicitly retry or supply a verified result", c.ID, c.Name))
		}
		if _, ok := r.Lookup(c.Name); !ok {
			return e.stop(ctx, t, Blocked, fmt.Sprintf("call %s references unknown or disallowed tool %s", c.ID, c.Name))
		}
		c.Started = true
		c.Approved = false
		t.Event("tool_started", c.ID+" "+c.Name)
		if err := e.save(ctx, t); err != nil {
			return err
		}
		raw, err := invoke(toolregistry.WithIdempotencyKey(ctx, c.Key), r, c.Name, c.Arguments)
		if err != nil {
			c.Error = err.Error()
			if c.Retry != toolregistry.ReadOnly && c.Retry != toolregistry.Idempotent {
				return e.stop(ctx, t, Blocked, fmt.Sprintf("call %s (%s) failed with an uncertain write result: %v", c.ID, c.Name, err))
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		c.Result = raw
		c.Done = true
		if c.Name == "report_progress" && err == nil {
			var in struct {
				Progress string     `json:"progress"`
				Plan     []PlanStep `json:"plan"`
			}
			if json.Unmarshal([]byte(raw), &in) == nil {
				t.Progress = in.Progress
				if in.Plan != nil {
					t.Plan = in.Plan
				}
				t.Event("progress", in.Progress)
			}
		}
		t.Event("tool_result", c.ID+" "+raw)
		if err = e.save(ctx, t); err != nil {
			return err
		}
	}
	var finish *Result
	for _, c := range cp.Calls {
		text := c.Result
		if c.Error != "" {
			data, _ := json.Marshal(map[string]string{"error": c.Error, "result": c.Result})
			text = string(data)
		}
		cp.Messages = append(cp.Messages, &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{CallID: c.ID, Name: c.Name, Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: text}}}})}})
		if c.Name == "finish_task" && c.Error == "" {
			var result Result
			if json.Unmarshal([]byte(c.Result), &result) == nil {
				finish = &result
			}
		}
	}
	cp.Calls = nil
	if finish != nil && !cp.Continue {
		t.Result = *finish
		if len(finish.Unmet) > 0 {
			return e.stop(ctx, t, Blocked, strings.Join(finish.Unmet, "; "))
		}
		return e.stop(ctx, t, Completed, finish.Summary)
	}
	cp.Continue = false
	return e.save(ctx, t)
}
func invoke(ctx context.Context, r *toolregistry.Registry, name, args string) (result string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("tool panic: %v", recovered)
		}
	}()
	return r.Invoke(ctx, name, args)
}

func (e *Engine) timeout(ctx context.Context, t *Task) error {
	for _, c := range t.Checkpoint.Calls {
		if c.Started && !c.Done && c.Retry != toolregistry.ReadOnly && c.Retry != toolregistry.Idempotent {
			return e.stop(ctx, t, Blocked, fmt.Sprintf("execution timed out; call %s (%s) has an uncertain write result", c.ID, c.Name))
		}
	}
	return e.stop(ctx, t, Failed, "execution time budget exhausted")
}
