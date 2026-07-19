package agenticmodel

import (
	"context"
	"easygo-agent/internal/agent"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/agenticdeepseek"
)

func NewAgenticDeepSeek(ctx context.Context, config agent.Config) (*agenticdeepseek.Model, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid deepseek config: %w", err)
	}
	return agenticdeepseek.New(ctx, config.ToAgenticDeepSeekConfig())
}
