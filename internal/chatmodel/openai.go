// Package chatmodel 构造模板使用的单个 Eino 聊天模型。
package chatmodel

import (
	"context"
	"fmt"
	"net/url"

	"easygo-agent/internal/config"
	openaiadapter "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"go.uber.org/zap"
)

// New 构造 OpenAI 兼容的 Eino ToolCallingChatModel。
func New(ctx context.Context, cfg config.ModelConfig, logger *zap.Logger) (model.ToolCallingChatModel, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	chatModel, err := openaiadapter.NewChatModel(ctx, toOpenAIConfig(cfg))
	if err != nil {
		wrappedErr := fmt.Errorf("create OpenAI-compatible chat model: %w", err)
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
func toOpenAIConfig(cfg config.ModelConfig) *openaiadapter.ChatModelConfig {
	return &openaiadapter.ChatModelConfig{
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
