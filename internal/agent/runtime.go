package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
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
	registry *Registry
}

// NewRuntimeFactory 创建绑定指定模型注册表的运行时工厂。
func NewRuntimeFactory(registry *Registry) *RuntimeFactory {
	return &RuntimeFactory{registry: registry}
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
	spec ModelSpec,
) (*adk.TypedRunner[*schema.AgenticMessage], error) {
	agenticModel, err := f.registry.Build(ctx, spec)
	if err != nil {
		return nil, err
	}
	return f.buildRunner(ctx, agenticModel)
}

// buildRunner 将 AgenticModel 包装为 TypedChatModelAgent，再创建启用流式输出的 TypedRunner。
func (f *RuntimeFactory) buildRunner(
	ctx context.Context,
	agenticModel model.AgenticModel,
) (*adk.TypedRunner[*schema.AgenticMessage], error) {
	_ = []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage](nil) // 工具/中间件注册点 TODO

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

	// CheckPointStore 用于跨轮次持久化 Agent 状态，暂未接入。
	var checkPointStore adk.CheckPointStore
	return adk.NewTypedRunner(adk.TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: checkPointStore,
	}), nil
}
