package agent

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/deepseek"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

const (
	ProviderDeepSeek = "deepseek" // DeepSeek 官方 API
	ProviderOpenAI   = "openai"   // OpenAI 及兼容协议供应商
	ProviderMiniMax  = "minimax"  // MiniMax（OpenAI 兼容协议）
)

// ModelSpec 描述构建聊天模型所需的连接与生成参数。
type ModelSpec struct {
	Provider        string         // 模型供应商标识，如 deepseek、openai
	APIKey          string         // 供应商 API 密钥
	Model           string         // 模型名称
	BaseURL         string         // 自定义 API 基址，空则使用供应商默认地址
	MaxOutputTokens uint32         // 单次回复最大输出 token 数
	Settings        map[string]any // 额外生成参数，如 temperature、top_p
}

// ModelBuilder 根据规格与 HTTP 客户端构造 Eino 聊天模型适配器。
type ModelBuilder func(context.Context, ModelSpec, *http.Client) (model.BaseChatModel, error)

// Registry 是唯一的模型构造入口。
// 每个注册的 builder 必须返回 Eino 模型适配器，不提供通用 HTTP 回退路径。
type Registry struct {
	httpClient *http.Client
	builders   map[string]ModelBuilder
}

// NewRegistry 创建注册表并预注册内置供应商（DeepSeek、OpenAI 兼容、MiniMax）。
func NewRegistry() *Registry {
	var transport http.RoundTripper = http.DefaultTransport
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}
	registry := &Registry{
		httpClient: &http.Client{Transport: transport},
		builders:   make(map[string]ModelBuilder),
	}
	registry.Register(ProviderDeepSeek, buildDeepSeek)
	registry.Register(ProviderOpenAI, buildOpenAICompatible)
	registry.Register(ProviderMiniMax, buildOpenAICompatible)
	return registry
}

// Register 注册指定供应商的模型构建函数，provider 会归一化为小写并去除首尾空白。
func (r *Registry) Register(provider string, builder ModelBuilder) {
	r.builders[strings.ToLower(strings.TrimSpace(provider))] = builder
}

// Supports 判断供应商是否已注册对应的 Eino 适配器。
func (r *Registry) Supports(provider string) bool {
	_, ok := r.builders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}

// Build 根据 ModelSpec 构造聊天模型；未注册的供应商或缺少必填字段时返回错误。
func (r *Registry) Build(ctx context.Context, spec ModelSpec) (model.BaseChatModel, error) {
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	builder, ok := r.builders[provider]
	if !ok {
		return nil, fmt.Errorf("provider %q has no registered Eino adapter", spec.Provider)
	}
	if strings.TrimSpace(spec.APIKey) == "" || strings.TrimSpace(spec.Model) == "" {
		return nil, fmt.Errorf("provider API key and model are required")
	}
	spec.Provider = provider
	return builder(ctx, spec, r.httpClient)
}

// buildDeepSeek 构造 DeepSeek 官方 Eino 聊天模型。
func buildDeepSeek(ctx context.Context, spec ModelSpec, client *http.Client) (model.BaseChatModel, error) {
	config := &deepseek.ChatModelConfig{
		APIKey:     spec.APIKey,
		Model:      spec.Model,
		HTTPClient: client,
		MaxTokens:  int(spec.MaxOutputTokens),
		BaseURL:    strings.TrimSpace(spec.BaseURL),
	}
	if value, ok := floatSetting(spec.Settings, "temperature"); ok {
		config.Temperature = float32(value)
	}
	if value, ok := floatSetting(spec.Settings, "top_p"); ok {
		config.TopP = float32(value)
	}
	return deepseek.NewChatModel(ctx, config)
}

// buildOpenAICompatible 构造 OpenAI 兼容协议的 Eino 聊天模型，供 OpenAI 与 MiniMax 共用。
func buildOpenAICompatible(ctx context.Context, spec ModelSpec, client *http.Client) (model.BaseChatModel, error) {
	maxTokens := int(spec.MaxOutputTokens)
	config := &einoopenai.ChatModelConfig{
		APIKey:              spec.APIKey,
		Model:               spec.Model,
		HTTPClient:          client,
		BaseURL:             strings.TrimRight(strings.TrimSpace(spec.BaseURL), "/"),
		MaxCompletionTokens: &maxTokens,
	}
	if value, ok := floatSetting(spec.Settings, "temperature"); ok {
		typed := float32(value)
		config.Temperature = &typed
	}
	if value, ok := floatSetting(spec.Settings, "top_p"); ok {
		typed := float32(value)
		config.TopP = &typed
	}
	return einoopenai.NewChatModel(ctx, config)
}

// floatSetting 从 Settings 中读取浮点型参数，兼容 float64、float32、int 类型。
func floatSetting(settings map[string]any, key string) (float64, bool) {
	if settings == nil {
		return 0, false
	}
	switch value := settings[key].(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	default:
		return 0, false
	}
}
