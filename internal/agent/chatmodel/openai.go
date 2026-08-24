// Package chatmodel 构造模板使用的单个 Eino Agentic 聊天模型。
package chatmodel

import (
	"context"
	"fmt"
	"net/url"

	"easygo-agent/internal/config"
	"easygo-agent/internal/logger"

	agenticopenai "github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"go.uber.org/zap"
)

// New 构造 OpenAI 兼容的 Eino AgenticModel，消息载体为 *schema.AgenticMessage。
func New(ctx context.Context, cfg config.ModelConfig) (model.AgenticModel, error) {
	chatModel, err := agenticopenai.NewChatModel(ctx, toOpenAIConfig(cfg))
	if err != nil {
		wrappedErr := fmt.Errorf("create OpenAI-compatible agentic chat model: %w", err)
		logger.Error("create chat model failed",
			zap.String("model", cfg.Name),
			zap.String("base_url_host", safeBaseURLHost(cfg.BaseURL)),
			zap.Error(wrappedErr),
		)
		return nil, wrappedErr
	}
	return chatModel, nil
}

// toOpenAIConfig 映射运行字段，不附加身份或授权策略。
func toOpenAIConfig(cfg config.ModelConfig) *agenticopenai.ChatConfig {
	return &agenticopenai.ChatConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Name,
		Timeout: cfg.Timeout,
	}
}

// safeBaseURLHost 提取用于诊断的非敏感 endpoint host。
func safeBaseURLHost(baseURL string) string {
	if baseURL == "" {
		return "default"
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return "invalid"
	}
	return parsedURL.Hostname()
}
