package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
)

type RuntimeConfig struct {
	Instruction string
	Revision    string
	TurnTimeout time.Duration
}

type RuntimeFactory struct {
	registry *Registry
	config   RuntimeConfig
}

func NewRuntimeFactory(registry *Registry, config RuntimeConfig) *RuntimeFactory {
	return &RuntimeFactory{registry: registry, config: config}
}

func (f *RuntimeFactory) Config() RuntimeConfig {
	return f.config
}

func (f *RuntimeFactory) Supports(provider string) bool {
	return f.registry.Supports(provider)
}

func (f *RuntimeFactory) Build(
	ctx context.Context,
	spec ModelSpec,
	maxContextTokens uint32,
) (*adk.Runner, error) {
	chatModel, err := f.registry.Build(ctx, spec)
	if err != nil {
		return nil, err
	}
	return f.buildRunner(ctx, chatModel, maxContextTokens, spec.MaxOutputTokens)
}

func (f *RuntimeFactory) buildRunner(
	ctx context.Context,
	chatModel model.BaseChatModel,
	maxContextTokens uint32,
	maxOutputTokens uint32,
) (*adk.Runner, error) {
	if maxContextTokens <= maxOutputTokens {
		return nil, fmt.Errorf("max context tokens must exceed max output tokens")
	}
	availableInput := int(maxContextTokens - maxOutputTokens)
	summaryThreshold := availableInput * 80 / 100
	if summaryThreshold < 1 {
		return nil, fmt.Errorf("model context budget is too small")
	}

	summaryRetries := 2
	summarizer, err := summarization.New(ctx, &summarization.Config{
		Model: chatModel,
		Trigger: &summarization.TriggerCondition{
			ContextTokens: summaryThreshold,
		},
		Retry: &summarization.RetryConfig{
			MaxRetries: &summaryRetries,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create Eino summarization middleware: %w", err)
	}

	chatAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "easygo-chat",
		Description: "EasyGo conversational assistant",
		Instruction: f.config.Instruction,
		Model:       chatModel,
		Handlers:    []adk.ChatModelAgentMiddleware{summarizer},
		ModelRetryConfig: &adk.ModelRetryConfig{
			MaxRetries: 2,
			ShouldRetry: func(_ context.Context, retry *adk.RetryContext) *adk.RetryDecision {
				return &adk.RetryDecision{
					Retry: retry != nil && retry.Err != nil && retry.OutputMessage == nil,
				}
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create Eino ChatModelAgent: %w", err)
	}

	return adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           chatAgent,
		EnableStreaming: true,
	}), nil
}
