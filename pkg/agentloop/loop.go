// Package agentloop runs a provider-neutral, sequential model/tool loop.
// Streaming deltas are provisional; Commit is the synchronous host-defined commit barrier.
package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"easygo-agent/pkg/ai"
)

var ErrStepLimit = errors.New("agent step allowance exhausted: final model requested tools")

type Tool struct {
	Definition ai.Tool
	Execute    func(context.Context, ai.Block) (string, error)
}

type Event struct {
	Type     string // model_delta, assistant, tool_started, tool_finished
	Delta    ai.Event
	Message  *ai.Message
	Response *ai.Response
	Call     ai.Block
}

type Config struct {
	Client      ai.Client
	Tools       []Tool
	MaxSteps    int           // model turns with tools enabled; one final synthesis turn follows
	MaxDuration time.Duration // hard deadline for the whole run, including synthesis
	BeforeModel func(context.Context, ai.Request) (ai.Request, error)
	Commit      func(context.Context, ai.Message) error
	Steering    func(context.Context) ([]ai.Message, error)
	// FatalToolError may preserve a host-specific interrupt instead of recovering it.
	FatalToolError func(error) bool
}

type Result struct {
	Messages []ai.Message
	Response ai.Response
	Steps    int
}

type Loop struct {
	cfg   Config
	tools map[string]Tool
}

func New(cfg Config) (*Loop, error) {
	if cfg.Client == nil || cfg.MaxSteps <= 0 || cfg.MaxDuration < 0 {
		return nil, errors.New("client and positive max steps required; duration cannot be negative")
	}
	l := &Loop{cfg: cfg, tools: make(map[string]Tool)}
	l.cfg.Tools = append([]Tool(nil), cfg.Tools...)
	for _, t := range cfg.Tools {
		if t.Definition.Name == "" || t.Execute == nil || !json.Valid(t.Definition.Parameters) {
			return nil, errors.New("tool requires a name, JSON schema, and execute function")
		}
		if _, exists := l.tools[t.Definition.Name]; exists {
			return nil, fmt.Errorf("duplicate tool %q", t.Definition.Name)
		}
		l.tools[t.Definition.Name] = t
	}
	return l, nil
}

// Run owns its history and runs tools sequentially. A failed assistant commit
// prevents all tool side effects; a failed tool-result commit prevents the next
// tool and the next model. Hooks must return only after their work is durable.
func (l *Loop) Run(ctx context.Context, req ai.Request, emit func(Event) error) (result Result, err error) {
	if l.cfg.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.cfg.MaxDuration)
		defer cancel()
	}
	req.Messages = cloneMessages(req.Messages)
	req.Tools = nil
	for _, t := range l.cfg.Tools {
		req.Tools = append(req.Tools, t.Definition)
	}
	send := func(e Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if emit != nil {
			return emit(e)
		}
		return nil
	}
	commit := func(m ai.Message) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if l.cfg.Commit != nil {
			if err := l.cfg.Commit(ctx, cloneMessages([]ai.Message{m})[0]); err != nil {
				return fmt.Errorf("commit %s: %w", m.Role, err)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		req.Messages = append(req.Messages, m)
		return nil
	}
	var pendingSteering []ai.Message
	steer := func(betweenTools bool) (bool, error) {
		if l.cfg.Steering == nil {
			return false, nil
		}
		messages, err := l.cfg.Steering(ctx)
		if err != nil {
			return false, err
		}
		if err = ctx.Err(); err != nil {
			return false, err
		}
		if betweenTools {
			pendingSteering = append(pendingSteering, cloneMessages(messages)...)
		} else {
			req.Messages = append(req.Messages, cloneMessages(messages)...)
		}
		return len(messages) > 0, nil
	}
	for step := 0; step <= l.cfg.MaxSteps; step++ {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		final := step == l.cfg.MaxSteps
		if l.cfg.BeforeModel != nil {
			req, err = l.cfg.BeforeModel(ctx, req)
			if err != nil {
				return result, err
			}
		}
		if final {
			req.Tools = nil
			req.ToolChoice = "none"
			req.Messages = append(req.Messages, ai.Message{Role: "system", Content: []ai.Block{{Type: "text", Text: "The execution allowance is exhausted. Give a final answer from the available results. Do not call tools."}}})
		}
		var delta func(ai.Event) error
		if emit != nil {
			delta = func(d ai.Event) error { return send(Event{Type: "model_delta", Delta: d}) }
		}
		response, callErr := l.cfg.Client.Complete(ctx, req, delta)
		result.Steps = step + 1
		if callErr != nil {
			return result, callErr
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if response.Message.Role != "assistant" {
			return result, fmt.Errorf("model returned role %q", response.Message.Role)
		}
		var calls []ai.Block
		for _, b := range response.Message.Content {
			if b.Type == "tool_call" {
				calls = append(calls, b)
			}
		}
		if final && len(calls) > 0 {
			return result, ErrStepLimit
		}
		if err = commit(response.Message); err != nil {
			return result, err
		}
		if err = send(Event{Type: "assistant", Message: &response.Message, Response: &response}); err != nil {
			return result, err
		}
		result.Response = response
		if len(calls) == 0 {
			if final {
				result.Messages = cloneMessages(req.Messages)
				return result, nil
			}
			more, e := steer(false)
			if e != nil {
				return result, e
			}
			if !more || final {
				result.Messages = cloneMessages(req.Messages)
				return result, nil
			}
			continue
		}
		for _, call := range calls {
			if err = send(Event{Type: "tool_started", Call: call}); err != nil {
				return result, err
			}
			var output string
			var toolErr error
			t, exists := l.tools[call.Name]
			var args map[string]json.RawMessage
			switch {
			case call.ID == "":
				toolErr = errors.New("tool call is missing id")
			case json.Unmarshal(call.Arguments, &args) != nil || args == nil:
				toolErr = errors.New("malformed tool arguments: expected JSON object")
			case !exists:
				toolErr = fmt.Errorf("unknown tool %q", call.Name)
			default:
				output, toolErr = t.Execute(ctx, call)
			}
			if err = ctx.Err(); err != nil {
				return result, err
			}
			if errors.Is(toolErr, context.Canceled) || errors.Is(toolErr, context.DeadlineExceeded) || (toolErr != nil && l.cfg.FatalToolError != nil && l.cfg.FatalToolError(toolErr)) {
				return result, toolErr
			}
			if toolErr != nil {
				output = fmt.Sprintf("[tool error] %v", toolErr)
			}
			m := ai.Message{Role: "tool", Content: []ai.Block{{Type: "tool_result", ID: call.ID, Name: call.Name, Text: output, IsError: toolErr != nil}}}
			if err = commit(m); err != nil {
				return result, err
			}
			if err = send(Event{Type: "tool_finished", Call: call, Message: &m}); err != nil {
				return result, err
			}
			if _, err = steer(true); err != nil {
				return result, err
			}
		}
		// Keep every tool result adjacent to its assistant call batch. Steering is
		// polled between side effects, then delivered to the next model afterward.
		req.Messages = append(req.Messages, pendingSteering...)
		pendingSteering = nil
	}
	return result, ErrStepLimit
}

func cloneMessages(in []ai.Message) []ai.Message {
	out := make([]ai.Message, len(in))
	for i, m := range in {
		out[i] = m
		out[i].Content = append([]ai.Block(nil), m.Content...)
		for j := range out[i].Content {
			out[i].Content[j].Arguments = append(json.RawMessage(nil), out[i].Content[j].Arguments...)
			out[i].Content[j].ProviderState = append(json.RawMessage(nil), out[i].Content[j].ProviderState...)
		}
	}
	return out
}
