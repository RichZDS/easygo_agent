// Package agent 用一层浅封装构造 Eino Deep Agent。
package deepagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/logger"
	"easygo-agent/internal/prompt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// Config supplies the adapters and policy needed to build a Deep Agent.
// Middleware composition and provider-specific behavior stay inside this module.
type Config struct {
	ChatModel    model.AgenticModel
	SummaryModel model.AgenticModel
	Tools        []tool.BaseTool
	Agent        config.AgentConfig
	Instruction  string
}

// New constructs a concurrent Eino Deep Agent using the supplied adapters.
func New(ctx context.Context, cfg Config) (adk.TypedAgent[*schema.AgenticMessage], error) {
	if cfg.ChatModel == nil {
		err := errors.New("chat model cannot be nil")
		logger.Error("create agent failed", zap.String("field", "chat_model"), zap.Error(err))
		return nil, err
	}
	if cfg.Agent.MaxSteps <= 0 {
		err := errors.New("max iteration must be greater than zero")
		logger.Error("create agent failed", zap.String("field", "max_iteration"), zap.Error(err))
		return nil, err
	}

	summaryModel := cfg.ChatModel
	if cfg.SummaryModel != nil {
		summaryModel = cfg.SummaryModel
	}
	threshold := cfg.Agent.ContextTokens
	if threshold <= 0 {
		threshold = 24000
	}
	// Eino's TypedChatModelAgent lazily builds a run closure the first time it
	// is used.  That closure contains per-run cancellation state, so sharing one
	// instance across concurrent conversations races inside Eino.  Keep the
	// public agent seam stable while creating an isolated Eino instance for every
	// execution.  This preserves parallel queue workers without exposing Eino's
	// mutable construction details to the application layer.
	tools := append([]tool.BaseTool(nil), cfg.Tools...)
	agentConfig := cfg.Agent
	build := func() (adk.TypedAgent[*schema.AgenticMessage], error) {
		compression, err := newCompression(ctx, summaryModel, threshold)
		if err != nil {
			return nil, err
		}
		instruction := strings.TrimSpace(cfg.Instruction)
		if instruction == "" {
			instruction = prompt.SystemPrompt
		}
		return deep.NewTyped(ctx, &deep.TypedConfig[*schema.AgenticMessage]{
			Name:                   "deep-agent",
			Instruction:            instruction,
			WithoutWriteTodos:      true,
			WithoutGeneralSubAgent: true,
			ChatModel:              cfg.ChatModel,
			ToolsConfig: adk.ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools:               tools,
					UnknownToolsHandler: unknownToolResult,
				},
			},
			MaxIteration: agentConfig.MaxSteps,
			Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{
				compression,
				&agentruntime.StateMiddleware{},
				newSafeToolMiddleware(),
			},
		})
	}
	if _, err := build(); err != nil {
		wrappedErr := fmt.Errorf("create Eino Deep Agent: %w", err)
		logger.Error("create agent failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return &isolatedAgent{build: build}, nil
}

// isolatedAgent is a small adapter around the Eino agent seam.  It owns no
// execution state itself; each Run gets a fresh Eino agent and therefore a
// private lazy run closure and middleware graph.
type isolatedAgent struct {
	build func() (adk.TypedAgent[*schema.AgenticMessage], error)
	mu    sync.Mutex
}

func (a *isolatedAgent) Name(ctx context.Context) string {
	return "deep-agent"
}

func (a *isolatedAgent) Description(ctx context.Context) string {
	return ""
}

func (a *isolatedAgent) Run(ctx context.Context, input *adk.TypedAgentInput[*schema.AgenticMessage], opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
	iterator, generator := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()

	// Build is normally cheap and independent, but serialize only construction
	// to protect adapters that lazily initialize shared provider clients.  The
	// returned Eino iterator runs without this lock, so sessions remain parallel.
	a.mu.Lock()
	agent, err := a.build()
	a.mu.Unlock()
	if err != nil {
		go func() {
			generator.Send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Err: err})
			generator.Close()
		}()
		return iterator
	}
	return agent.Run(ctx, input, opts...)
}
