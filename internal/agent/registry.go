package agent

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
)

const (
	ProviderDeepSeek = "deepseek"
	ProviderOpenAI   = "openai"
	ProviderMiniMax  = "minimax"
)

// ModelSpec 描述构建 AgenticModel 所需的连接参数，由 providerconfig 层从 DB 解析后传入。
type ModelSpec struct {
	Provider        string         // 供应商标识，如 deepseek / openai / minimax
	APIKey          string         // 解密后的 API Key
	Model           string         // 模型 ID，如 deepseek-chat、gpt-4o
	BaseURL         string         // API 基址；OpenAI-compatible 供应商必填（MiniMax 等）
	MaxOutputTokens uint32         // 单次回复 token 上限，0 表示使用模型默认值
	Settings        map[string]any // 可选采样参数，如 temperature、top_p
}

// ModelBuilder 根据 ModelSpec 构造 Eino model.AgenticModel 实例。
type ModelBuilder func(context.Context, ModelSpec, *http.Client) (model.AgenticModel, error)

// Registry 是 LLM 客户端的唯一构造入口；按 provider 路由到对应的 ModelBuilder。
type Registry struct {
	httpClient *http.Client              // 共享 HTTP 客户端，所有 provider 复用同一 Transport
	builders   map[string]ModelBuilder // provider 名称（小写）→ 构建函数
}

// NewRegistry 创建注册表并预注册内置 provider。
// 当前 DeepSeek、OpenAI、MiniMax 均走 OpenAI-compatible adapter（agenticopenai）。
func NewRegistry() *Registry {
	// Clone Transport 避免与其他 http.Client 共享连接池状态。
	var transport http.RoundTripper = http.DefaultTransport
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}
	registry := &Registry{
		httpClient: &http.Client{Transport: transport},
		builders:   make(map[string]ModelBuilder),
	}
	registry.Register(ProviderDeepSeek, buildAgenticOpenAICompatible)
	registry.Register(ProviderOpenAI, buildAgenticOpenAICompatible)
	registry.Register(ProviderMiniMax, buildAgenticOpenAICompatible)
	return registry
}

// Register 注册 provider 对应的 ModelBuilder；provider 名称会被规范化为小写。
func (r *Registry) Register(provider string, builder ModelBuilder) {
	r.builders[strings.ToLower(strings.TrimSpace(provider))] = builder
}

// Supports 检查给定 provider 是否已注册 adapter。
func (r *Registry) Supports(provider string) bool {
	_, ok := r.builders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}

// Build 根据 ModelSpec 构造 AgenticModel，供 RuntimeFactory 包装为 TypedChatModelAgent。
func (r *Registry) Build(ctx context.Context, spec ModelSpec) (model.AgenticModel, error) {
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	builder, ok := r.builders[provider]
	if !ok {
		return nil, fmt.Errorf("provider %q has no registered AgenticModel adapter", spec.Provider)
	}
	if strings.TrimSpace(spec.APIKey) == "" || strings.TrimSpace(spec.Model) == "" {
		return nil, fmt.Errorf("provider API key and model are required")
	}
	spec.Provider = provider
	// DeepSeek 未配置 BaseURL 时使用官方默认地址。
	if provider == ProviderDeepSeek && strings.TrimSpace(spec.BaseURL) == "" {
		spec.BaseURL = "https://api.deepseek.com"
	}
	return builder(ctx, spec, r.httpClient)
}

// buildAgenticOpenAICompatible 通过 Eino agenticopenai adapter 创建 AgenticModel。
// 实际 HTTP 调用（chat/completions）在 eino-ext 库内部完成，本函数只负责映射配置。
func buildAgenticOpenAICompatible(ctx context.Context, spec ModelSpec, client *http.Client) (model.AgenticModel, error) {
	config := &agenticopenai.ChatConfig{
		APIKey:     spec.APIKey,
		Model:      spec.Model,
		HTTPClient: client,
		BaseURL:    strings.TrimRight(strings.TrimSpace(spec.BaseURL), "/"),
	}
	if spec.MaxOutputTokens > 0 {
		maxTokens := int(spec.MaxOutputTokens)
		config.MaxCompletionTokens = &maxTokens
	}
	if value, ok := floatSetting(spec.Settings, "temperature"); ok {
		typed := float32(value)
		config.Temperature = &typed
	}
	if value, ok := floatSetting(spec.Settings, "top_p"); ok {
		typed := float32(value)
		config.TopP = &typed
	}
	return agenticopenai.NewChatModel(ctx, config)
}

// floatSetting 从 Settings map 中提取浮点参数，兼容 JSON 反序列化后的多种数值类型。
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
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}
