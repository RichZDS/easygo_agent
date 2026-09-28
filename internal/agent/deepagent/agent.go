// Package deepagent adapts the native loop to the existing runtime message seam.
package deepagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"easygo-agent/internal/agent/chatmodel"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/config"
	"easygo-agent/internal/prompt"
	"easygo-agent/pkg/agentloop"
	"easygo-agent/services/ai-gateway/ai"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type Config struct {
	ChatModel    model.AgenticModel
	SummaryModel model.AgenticModel
	Tools        []tool.BaseTool
	Agent        config.AgentConfig
	Instruction  string
}

type nativeAgent struct {
	cfg   Config
	tools []agentloop.Tool
	infos []*schema.ToolInfo
}

func New(ctx context.Context, cfg Config) (adk.TypedAgent[*schema.AgenticMessage], error) {
	if cfg.ChatModel == nil {
		return nil, errors.New("chat model cannot be nil")
	}
	if cfg.Agent.MaxSteps <= 0 {
		return nil, errors.New("max iteration must be greater than zero")
	}
	if cfg.Agent.ContextTokens <= 0 {
		cfg.Agent.ContextTokens = 24000
	}
	if cfg.SummaryModel == nil {
		cfg.SummaryModel = cfg.ChatModel
	}
	if strings.TrimSpace(cfg.Instruction) == "" {
		cfg.Instruction = prompt.SystemPrompt
	}
	cfg.ChatModel = telemetry.Model(cfg.ChatModel, "main")
	cfg.SummaryModel = telemetry.Model(cfg.SummaryModel, "context-compressor")
	a := &nativeAgent{cfg: cfg}
	for _, t := range cfg.Tools {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}
		defs, err := chatmodel.ToolsToAI([]*schema.ToolInfo{info})
		if err != nil {
			return nil, err
		}
		inv, invoke := t.(tool.InvokableTool)
		stream, streamable := t.(tool.StreamableTool)
		if !invoke && !streamable {
			return nil, fmt.Errorf("tool %q has no supported execution interface", info.Name)
		}
		a.infos = append(a.infos, info)
		a.tools = append(a.tools, agentloop.Tool{Definition: defs[0], Execute: func(ctx context.Context, call ai.Block) (out string, err error) {
			ctx, span := telemetry.Start(ctx, "tool", call.Name, zap.String("call_id", call.ID))
			defer func() {
				status := ""
				if err != nil && !isFatalToolError(err) {
					status = "recovered"
				}
				span.Finish(status, err, zap.Int("result_bytes", len(out)))
			}()
			if invoke {
				return inv.InvokableRun(ctx, string(call.Arguments))
			}
			reader, e := stream.StreamableRun(ctx, string(call.Arguments))
			if e != nil {
				return "", e
			}
			if reader == nil {
				return "", errors.New("tool returned a nil stream")
			}
			defer reader.Close()
			var text strings.Builder
			for {
				chunk, e := reader.Recv()
				if errors.Is(e, io.EOF) {
					return text.String(), nil
				}
				if e != nil {
					return "", e
				}
				if e = ctx.Err(); e != nil {
					return "", e
				}
				text.WriteString(chunk)
			}
		}})
	}
	// Validate duplicate names and schemas before accepting construction.
	if _, err := agentloop.New(agentloop.Config{Client: &modelBridge{}, Tools: a.tools, MaxSteps: cfg.Agent.MaxSteps}); err != nil {
		return nil, err
	}
	return a, nil
}
func (a *nativeAgent) Name(context.Context) string        { return "deep-agent" }
func (a *nativeAgent) Description(context.Context) string { return "" }
func (a *nativeAgent) Run(ctx context.Context, input *adk.TypedAgentInput[*schema.AgenticMessage], opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
	go func() {
		defer gen.Close()
		send := func(e *adk.TypedAgentEvent[*schema.AgenticMessage]) { e.AgentName = "deep-agent"; gen.Send(e) }
		fail := func(err error) { send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Err: err}) }
		if input == nil {
			fail(errors.New("agent input required"))
			return
		}
		messages := append([]*schema.AgenticMessage(nil), input.Messages...)
		if len(messages) == 0 || messages[0].Role != schema.AgenticRoleTypeSystem {
			messages = append([]*schema.AgenticMessage{schema.SystemAgenticMessage(a.cfg.Instruction)}, messages...)
		}
		b := &modelBridge{model: a.cfg.ChatModel, messages: messages, infos: append([]*schema.ToolInfo(nil), a.infos...), output: func(v *adk.TypedMessageVariant[*schema.AgenticMessage]) {
			send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Output: &adk.TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: v}})
		}}
		event := func(kind summarization.ActionType) {
			send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Action: &adk.AgentAction{CustomizedAction: &summarization.TypedCustomizedAction[*schema.AgenticMessage]{Type: kind}}})
		}
		loop, err := agentloop.New(agentloop.Config{Client: b, Tools: a.tools, MaxSteps: a.cfg.Agent.MaxSteps, FatalToolError: isFatalToolError,
			BeforeModel: func(ctx context.Context, r ai.Request) (ai.Request, error) {
				fitted, infos, err := fitContext(ctx, b.messages, b.infos, a.cfg.SummaryModel, a.cfg.Agent.ContextTokens, event)
				if err != nil {
					return r, err
				}
				b.messages = fitted
				b.infos = infos
				r.Messages, err = chatmodel.MessagesToAI(fitted)
				if err != nil {
					return r, err
				}
				r.Tools, err = chatmodel.ToolsToAI(infos)
				return r, err
			},
			Commit: func(ctx context.Context, m ai.Message) error {
				var message *schema.AgenticMessage
				if m.Role == "assistant" {
					message = b.last
				} else {
					message = chatmodel.MessageFromAI(m)
				}
				b.messages = append(b.messages, message)
				if err := agentruntime.CaptureState(ctx, b.messages); err != nil {
					return err
				}
				if m.Role == "tool" {
					b.output(&adk.TypedMessageVariant[*schema.AgenticMessage]{Message: message, AgenticRole: message.Role})
				}
				return nil
			},
		})
		if err != nil {
			fail(err)
			return
		}
		var emit func(agentloop.Event) error
		if input.EnableStreaming {
			emit = func(agentloop.Event) error { return nil }
		}
		if _, err = loop.Run(ctx, ai.Request{}, emit); err != nil {
			fail(err)
		}
	}()
	return iter
}

// modelBridge is per-run. It retains the original schema messages (including
// provider metadata) while exposing canonical execution to the native loop.
type modelBridge struct {
	model    model.AgenticModel
	messages []*schema.AgenticMessage
	infos    []*schema.ToolInfo
	last     *schema.AgenticMessage
	output   func(*adk.TypedMessageVariant[*schema.AgenticMessage])
}

func (b *modelBridge) Complete(ctx context.Context, r ai.Request, emit func(ai.Event) error) (ai.Response, error) {
	input := b.messages
	opts := []model.Option{model.WithTools(b.infos)}
	if r.ToolChoice == "none" {
		opts = []model.Option{model.WithTools(nil), model.WithToolChoice(schema.ToolChoiceForbidden)}
		input = append(append([]*schema.AgenticMessage(nil), input...), chatmodel.MessageFromAI(r.Messages[len(r.Messages)-1]))
	}
	var message *schema.AgenticMessage
	var err error
	if emit == nil {
		message, err = b.model.Generate(ctx, input, opts...)
		if err == nil && message != nil {
			b.output(&adk.TypedMessageVariant[*schema.AgenticMessage]{Message: message, AgenticRole: message.Role})
		}
	} else {
		source, e := b.model.Stream(ctx, input, opts...)
		if e != nil {
			return ai.Response{}, e
		}
		if source == nil {
			return ai.Response{}, errors.New("model returned a nil stream")
		}
		defer source.Close()
		reader, writer := schema.Pipe[*schema.AgenticMessage](16)
		b.output(&adk.TypedMessageVariant[*schema.AgenticMessage]{IsStreaming: true, MessageStream: reader, AgenticRole: schema.AgenticRoleTypeAssistant})
		defer writer.Close()
		var chunks []*schema.AgenticMessage
		for {
			chunk, e := source.Recv()
			if errors.Is(e, io.EOF) {
				break
			}
			if e != nil {
				_ = writer.Send(nil, e)
				return ai.Response{}, e
			}
			if e = ctx.Err(); e != nil {
				return ai.Response{}, e
			}
			if chunk == nil {
				continue
			}
			chunks = append(chunks, chunk)
			if writer.Send(chunk, nil) {
				return ai.Response{}, io.ErrClosedPipe
			}
		}
		if len(chunks) > 0 {
			message, err = schema.ConcatAgenticMessages(chunks)
		}
	}
	if err != nil {
		return ai.Response{}, err
	}
	if err = ctx.Err(); err != nil {
		return ai.Response{}, err
	}
	if message == nil {
		return ai.Response{}, errors.New("model returned no message")
	}
	canonical, err := chatmodel.MessagesToAI([]*schema.AgenticMessage{message})
	if err != nil {
		return ai.Response{}, err
	}
	if len(canonical) != 1 {
		return ai.Response{}, errors.New("model returned mixed roles")
	}
	b.last = message
	return ai.Response{Message: canonical[0]}, nil
}
