package chatmodel

import (
	"context"
	"easygo-agent/internal/agent"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/deepseek"
)

func NewChatModelDeepSeek(ctx context.Context, config agent.Config) (*deepseek.ChatModel, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid deepseek config: %w", err)
	}
	return deepseek.NewChatModel(ctx, config.ToChatModelConfig())
}
