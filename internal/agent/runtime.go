package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
)

// RuntimeConfig 定义 Agent 运行时的全局行为参数。
type RuntimeConfig struct {
	Instruction string        // 注入 ChatModelAgent 的系统指令
	Revision    string        // 配置版本标识，便于追踪变更
	TurnTimeout time.Duration // 单轮对话超时时间
}

// RuntimeFactory 基于 Registry 组装 Eino ADK Runner，负责模型实例化与中间件装配。
type RuntimeFactory struct {
	registry *Registry
	config   RuntimeConfig
}

// NewRuntimeFactory 创建运行时工厂，依赖已配置好的模型注册表。
func NewRuntimeFactory(registry *Registry, config RuntimeConfig) *RuntimeFactory {
	return &RuntimeFactory{registry: registry, config: config}
}

// Config 返回当前运行时配置副本。
func (f *RuntimeFactory) Config() RuntimeConfig {
	return f.config
}

// Supports 委托 Registry 判断供应商是否可用。
func (f *RuntimeFactory) Supports(provider string) bool {
	return f.registry.Supports(provider)
}

// Build 根据 ModelSpec 构造可流式运行的 Eino ADK Runner。
// maxContextTokens 为模型上下文窗口上限，用于计算摘要触发阈值。
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

// buildRunner 装配 ChatModelAgent 与摘要中间件，并包装为 ADK Runner。
func (f *RuntimeFactory) buildRunner(
	ctx context.Context,
	chatModel model.BaseChatModel,
	maxContextTokens uint32,
	maxOutputTokens uint32,
) (*adk.Runner, error) {
	if maxContextTokens <= maxOutputTokens {
		return nil, fmt.Errorf("max context tokens must exceed max output tokens")
	}
	// 预留输出 token 后，剩余部分作为可用输入上下文。
	availableInput := int(maxContextTokens - maxOutputTokens)
	// 当上下文 token 达到可用输入的 80% 时触发自动摘要，防止窗口溢出。
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
			// 仅在模型调用出错且未产生任何输出消息时重试，避免重复回复。
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
