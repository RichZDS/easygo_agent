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
	ProviderDeepSeek = "deepseek"
	ProviderOpenAI   = "openai"
	ProviderMiniMax  = "minimax"
)

type ModelSpec struct {
	Provider        string
	APIKey          string
	Model           string
	BaseURL         string
	MaxOutputTokens uint32
	Settings        map[string]any
}

type ModelBuilder func(context.Context, ModelSpec, *http.Client) (model.BaseChatModel, error)

// Registry is the only model construction seam. Every registered builder must
// return an Eino model adapter; there is deliberately no generic HTTP fallback.
type Registry struct {
	httpClient *http.Client
	builders   map[string]ModelBuilder
}

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

func (r *Registry) Register(provider string, builder ModelBuilder) {
	r.builders[strings.ToLower(strings.TrimSpace(provider))] = builder
}

func (r *Registry) Supports(provider string) bool {
	_, ok := r.builders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}

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
