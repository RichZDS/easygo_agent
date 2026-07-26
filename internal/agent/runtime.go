package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	DefaultInstruction     = "You are EasyGo, a helpful assistant. Provide clear, accurate, and concise responses."
	DefaultRunTimeout      = 5 * time.Minute
	DefaultContextMessageN = 40
	AgentName              = "easygo-chat"
)

// RuntimeFactory builds TypedRunner[*schema.AgenticMessage].
type RuntimeFactory struct {
	registry *Registry
}

func NewRuntimeFactory(registry *Registry) *RuntimeFactory {
	return &RuntimeFactory{registry: registry}
}

func (f *RuntimeFactory) Supports(provider string) bool {
	return f.registry.Supports(provider)
}

func (f *RuntimeFactory) RunTimeout() time.Duration {
	return DefaultRunTimeout
}

func (f *RuntimeFactory) ContextMessageLimit() int {
	return DefaultContextMessageN
}

// Build constructs a streaming TypedRunner. CheckPointStore is reserved (nil TODO).
// Tools registration point is reserved (empty list TODO).
func (f *RuntimeFactory) Build(
	ctx context.Context,
	spec ModelSpec,
) (*adk.TypedRunner[*schema.AgenticMessage], error) {
	agenticModel, err := f.registry.Build(ctx, spec)
	if err != nil {
		return nil, err
	}
	return f.buildRunner(ctx, agenticModel)
}

func (f *RuntimeFactory) buildRunner(
	ctx context.Context,
	agenticModel model.AgenticModel,
) (*adk.TypedRunner[*schema.AgenticMessage], error) {
	_ = []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage](nil) // Tools/handlers TODO

	chatAgent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        AgentName,
		Description: "EasyGo conversational assistant",
		Instruction: DefaultInstruction,
		Model:       agenticModel,
		ToolsConfig: adk.ToolsConfig{}, // TODO: register tools
		ModelRetryConfig: &adk.TypedModelRetryConfig[*schema.AgenticMessage]{
			MaxRetries: 2,
			ShouldRetry: func(_ context.Context, retry *adk.TypedRetryContext[*schema.AgenticMessage]) *adk.TypedRetryDecision[*schema.AgenticMessage] {
				return &adk.TypedRetryDecision[*schema.AgenticMessage]{
					Retry: retry != nil && retry.Err != nil && any(retry.OutputMessage) == nil,
				}
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create TypedChatModelAgent: %w", err)
	}

	// CheckPointStore: reserved, nil for now (TODO).
	var checkPointStore adk.CheckPointStore
	return adk.NewTypedRunner(adk.TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           chatAgent,
		EnableStreaming: true,
		CheckPointStore: checkPointStore,
	}), nil
}
