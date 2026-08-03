package agent

import (
	"context"
	"fmt"
	"time"

	"easygo-agent/internal/platform/logger"
	"easygo-agent/internal/skill/workspace"
	"github.com/cloudwego/eino/adk"
	filesystemmw "github.com/cloudwego/eino/adk/middlewares/filesystem"
	skillmw "github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	// DefaultInstruction 是聊天 Agent 的系统提示词。
	DefaultInstruction = "You are EasyGo, a helpful assistant. Provide clear, accurate, and concise responses."
	// DefaultRunTimeout 单次 Agent 运行的最长时间。
	DefaultRunTimeout = 5 * time.Minute
	// DefaultContextMessageN 每次运行带入的最近会话消息条数上限。
	DefaultContextMessageN = 40
	// AgentName 是 Eino ADK 中标识本 Agent 的名称。
	AgentName = "easygo-chat"
)

// RuntimeFactory 根据 ModelSpec 组装 Eino ADK 的 TypedRunner。
type RuntimeFactory struct {
	registry   *Registry
	workspaces *workspace.Manager
}

// NewRuntimeFactory 创建绑定模型注册表和用户 Skill workspace 的运行时工厂。
func NewRuntimeFactory(registry *Registry, workspaces *workspace.Manager) *RuntimeFactory {
	return &RuntimeFactory{registry: registry, workspaces: workspaces}
}

// Supports 报告注册表是否支持给定 provider。
func (f *RuntimeFactory) Supports(provider string) bool {
	return f.registry.Supports(provider)
}

// RunTimeout 返回单次 Agent 运行的超时时间，供 execution 层设置 deadline。
func (f *RuntimeFactory) RunTimeout() time.Duration {
	return DefaultRunTimeout
}

// ContextMessageLimit 返回会话上下文消息条数上限，供 run 层裁剪历史记录。
func (f *RuntimeFactory) ContextMessageLimit() int {
	return DefaultContextMessageN
}

// Build 根据 ModelSpec 构建流式 TypedRunner。
// CheckPointStore 与 Tools 注册点已预留，当前分别为 nil 与空列表。
func (f *RuntimeFactory) Build(
	ctx context.Context,
	userID uint64,
	spec ModelSpec,
) (*adk.TypedRunner[*schema.AgenticMessage], error) {
	agenticModel, err := f.registry.Build(ctx, spec)
	if err != nil {
		logger.ErrorContext(ctx, "build agent runtime failed", zap.Uint64("user_id", userID), zap.String("provider", spec.Provider), zap.Error(err))
		return nil, err
	}
	handlers, err := f.buildSkillHandlers(ctx, userID)
	if err != nil {
		logger.ErrorContext(ctx, "build agent runtime failed", zap.Uint64("user_id", userID), zap.Error(err))
		return nil, err
	}
	runner, err := f.buildRunner(ctx, agenticModel, handlers)
	if err != nil {
		logger.ErrorContext(ctx, "build agent runtime failed", zap.Uint64("user_id", userID), zap.Error(err))
		return nil, err
	}
	return runner, nil
}

// buildSkillHandlers creates a fresh user-scoped Skill catalog and read-only file tools for one Turn.
func (f *RuntimeFactory) buildSkillHandlers(
	ctx context.Context,
	userID uint64,
) ([]adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage], error) {
	if f.workspaces == nil {
		err := fmt.Errorf("skill workspace manager is required")
		logger.ErrorContext(ctx, "build agent skill handlers failed", zap.Uint64("user_id", userID), zap.Error(err))
		return nil, err
	}
	backend, err := f.workspaces.NewBackend(ctx, userID)
	if err != nil {
		logger.ErrorContext(ctx, "build agent skill handlers failed", zap.Uint64("user_id", userID), zap.Error(err))
		return nil, err
	}
	skillBackend, err := skillmw.NewBackendFromFilesystem(ctx, &skillmw.BackendFromFilesystemConfig{
		Backend: backend,
		BaseDir: "/",
	})
	if err != nil {
		wrappedErr := fmt.Errorf("create Eino skill backend: %w", err)
		logger.ErrorContext(ctx, "build agent skill handlers failed", zap.Uint64("user_id", userID), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	if _, err := skillBackend.List(ctx); err != nil {
		wrappedErr := fmt.Errorf("validate Eino skill catalog: %w", err)
		logger.ErrorContext(ctx, "build agent skill handlers failed", zap.Uint64("user_id", userID), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	skillHandler, err := skillmw.NewTyped[*schema.AgenticMessage](ctx, &skillmw.TypedConfig[*schema.AgenticMessage]{
		Backend: skillBackend,
	})
	if err != nil {
		wrappedErr := fmt.Errorf("create Eino skill handler: %w", err)
		logger.ErrorContext(ctx, "build agent skill handlers failed", zap.Uint64("user_id", userID), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	filesystemHandler, err := filesystemmw.NewTyped[*schema.AgenticMessage](ctx, &filesystemmw.MiddlewareConfig{
		Backend: backend,
		WriteFileToolConfig: &filesystemmw.ToolConfig{
			Disable: true,
		},
		EditFileToolConfig: &filesystemmw.ToolConfig{
			Disable: true,
		},
	})
	if err != nil {
		wrappedErr := fmt.Errorf("create Eino read-only filesystem handler: %w", err)
		logger.ErrorContext(ctx, "build agent skill handlers failed", zap.Uint64("user_id", userID), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{filesystemHandler, skillHandler}, nil
}

// buildRunner 将 AgenticModel 包装为 TypedChatModelAgent，再创建启用流式输出的 TypedRunner。
func (f *RuntimeFactory) buildRunner(
	ctx context.Context,
	agenticModel model.AgenticModel,
	handlers []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage],
) (*adk.TypedRunner[*schema.AgenticMessage], error) {
	// chatAgent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{
	// 	Name:        AgentName,
	// 	Description: "EasyGo conversational assistant",
	// 	Instruction: DefaultInstruction,
	// 	Model:       agenticModel,
	// 	ToolsConfig: adk.ToolsConfig{}, // TODO: register tools
	// 	ModelRetryConfig: &adk.TypedModelRetryConfig[*schema.AgenticMessage]{
	// 		MaxRetries: 2,
	// 		// 仅在模型调用出错且未产出任何消息时重试，避免重复返回已有内容。
	// 		ShouldRetry: func(_ context.Context, retry *adk.TypedRetryContext[*schema.AgenticMessage]) *adk.TypedRetryDecision[*schema.AgenticMessage] {
	// 			return &adk.TypedRetryDecision[*schema.AgenticMessage]{
	// 				Retry: retry != nil && retry.Err != nil && any(retry.OutputMessage) == nil,
	// 			}
	// 		},
	// 	},
	// })

	// 升级为 Deep Agent
	agent, err := deep.NewTyped(ctx, &deep.TypedConfig[*schema.AgenticMessage]{
		Name:                   AgentName,
		Description:            "EasyGo conversational assistant",
		ChatModel:              agenticModel,
		Instruction:            DefaultInstruction, // 或刻意留空用 Deep 内置 Prompt
		WithoutWriteTodos:      true,               // 先验证链路可关
		WithoutGeneralSubAgent: true,
		Handlers:               handlers,
		ModelRetryConfig: &adk.TypedModelRetryConfig[*schema.AgenticMessage]{
			MaxRetries: 2,
			// shouldRetryModelCall retries only failures that produced no output message.
			ShouldRetry: func(_ context.Context, retry *adk.TypedRetryContext[*schema.AgenticMessage]) *adk.TypedRetryDecision[*schema.AgenticMessage] {
				return &adk.TypedRetryDecision[*schema.AgenticMessage]{
					Retry: retry != nil && retry.Err != nil && any(retry.OutputMessage) == nil,
				}
			},
		},
	})
	if err != nil {
		wrappedErr := fmt.Errorf("create TypedChatModelAgent: %w", err)
		logger.ErrorContext(ctx, "build typed runner failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}

	// CheckPointStore 用于跨轮次持久化 Agent 状态，暂未接入。
	var checkPointStore adk.CheckPointStore
	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: checkPointStore,
	})
	return runner, nil
}
