// Package agent 用一层浅封装构造 Eino Deep Agent。
package deepagent

import (
	"context"
	"errors"
	"fmt"

	"easygo-agent/internal/config"
	"easygo-agent/internal/logger"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// New 构造可并发调用的 Eino Deep Agent，消息类型为 *schema.AgenticMessage。
func New(ctx context.Context, chatModel model.AgenticModel, tools []tool.BaseTool, cfg config.AgentConfig) (adk.TypedAgent[*schema.AgenticMessage], error) {
	if chatModel == nil {
		err := errors.New("chat model cannot be nil")
		logger.Error("create agent failed", zap.String("field", "chat_model"), zap.Error(err))
		return nil, err
	}
	if cfg.MaxSteps <= 0 {
		err := errors.New("max iteration must be greater than zero")
		logger.Error("create agent failed", zap.String("field", "max_iteration"), zap.Error(err))
		return nil, err
	}

	agent, err := deep.NewTyped(ctx, &deep.TypedConfig[*schema.AgenticMessage]{
		Name:      "deep-agent",
		ChatModel: chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               tools,
				UnknownToolsHandler: unknownToolResult,
			},
		},
		MaxIteration: cfg.MaxSteps,
		Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{
			newSafeToolMiddleware(),
		},
	})
	if err != nil {
		wrappedErr := fmt.Errorf("create Eino Deep Agent: %w", err)
		logger.Error("create agent failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return agent, nil
}
