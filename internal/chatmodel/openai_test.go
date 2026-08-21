package chatmodel

import (
	"testing"
	"time"

	"easygo-agent/internal/config"
)

// TestToOpenAIConfig 验证单模型配置映射，且不带策略字段。
func TestToOpenAIConfig(t *testing.T) {
	t.Parallel()

	input := config.ModelConfig{
		Name:    "test-model",
		BaseURL: "https://example.com/v1",
		Timeout: 45 * time.Second,
		APIKey:  "test-key",
	}
	got := toOpenAIConfig(input)
	if got.Model != input.Name {
		t.Errorf("Model = %q, want %q", got.Model, input.Name)
	}
	if got.BaseURL != input.BaseURL {
		t.Errorf("BaseURL = %q, want %q", got.BaseURL, input.BaseURL)
	}
	if got.Timeout != input.Timeout {
		t.Errorf("Timeout = %s, want %s", got.Timeout, input.Timeout)
	}
	if got.APIKey != input.APIKey {
		t.Error("APIKey was not mapped")
	}
	if got.User != nil || got.ByAzure {
		t.Error("unexpected identity or provider policy fields were set")
	}
}
