package agent

import (
	"context"
	"testing"
)

func TestRegistryOnlySupportsRegisteredEinoAdapters(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	for _, provider := range []string{ProviderDeepSeek, ProviderOpenAI, ProviderMiniMax, " DeepSeek "} {
		if !registry.Supports(provider) {
			t.Fatalf("expected provider %q to be registered", provider)
		}
	}
	if registry.Supports("custom-http") {
		t.Fatal("unregistered provider must not be accepted")
	}
	if _, err := registry.Build(context.Background(), ModelSpec{
		Provider: "custom-http",
		APIKey:   "secret",
		Model:    "model",
	}); err == nil {
		t.Fatal("expected an unregistered provider to fail without an HTTP fallback")
	}
}
