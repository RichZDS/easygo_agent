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

// ModelSpec describes AgenticModel connection parameters.
type ModelSpec struct {
	Provider        string
	APIKey          string
	Model           string
	BaseURL         string
	MaxOutputTokens uint32
	Settings        map[string]any
}

// ModelBuilder constructs an AgenticModel for a provider.
type ModelBuilder func(context.Context, ModelSpec, *http.Client) (model.AgenticModel, error)

// Registry is the only model construction entrypoint.
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
	registry.Register(ProviderDeepSeek, buildAgenticOpenAICompatible)
	registry.Register(ProviderOpenAI, buildAgenticOpenAICompatible)
	registry.Register(ProviderMiniMax, buildAgenticOpenAICompatible)
	return registry
}

func (r *Registry) Register(provider string, builder ModelBuilder) {
	r.builders[strings.ToLower(strings.TrimSpace(provider))] = builder
}

func (r *Registry) Supports(provider string) bool {
	_, ok := r.builders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}

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
	if provider == ProviderDeepSeek && strings.TrimSpace(spec.BaseURL) == "" {
		spec.BaseURL = "https://api.deepseek.com"
	}
	return builder(ctx, spec, r.httpClient)
}

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
