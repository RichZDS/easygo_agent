package deepagent

import (
	"context"
	"fmt"

	"easygo-agent/internal/agent"

	"github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
)

func NewDeepAgentDeepSeek(ctx context.Context, config agent.Config) (adk.ResumableAgent, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid deepseek config: %w", err)
	}

	chatModel, err := deepseek.NewChatModel(ctx, config.ToChatModelConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create chat model: %w", err)
	}

	deepAgent, err := deep.New(ctx, &deep.Config{
		Name:      "DeepAgent",
		ChatModel: chatModel,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create deep agent: %w", err)
	}
	return deepAgent, nil
}
