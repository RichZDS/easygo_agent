package agent

import "testing"

// TestDefaultBaseURL 校验各 Provider 的官方默认基址。
func TestDefaultBaseURL(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{provider: ProviderDeepSeek, want: "https://api.deepseek.com"},
		{provider: ProviderOpenAI, want: "https://api.openai.com/v1"},
		{provider: ProviderMiniMax, want: "https://api.minimaxi.com/v1"},
	}
	for _, test := range tests {
		if got := DefaultBaseURL(test.provider); got != test.want {
			t.Fatalf("DefaultBaseURL(%q) = %q, want %q", test.provider, got, test.want)
		}
	}
}

// TestModelsURL 校验模型列表地址拼接规则。
func TestModelsURL(t *testing.T) {
	if got := ModelsURL("https://api.openai.com/v1"); got != "https://api.openai.com/v1/models" {
		t.Fatalf("ModelsURL() = %q, want %q", got, "https://api.openai.com/v1/models")
	}
	if got := ModelsURL("https://api.deepseek.com/"); got != "https://api.deepseek.com/models" {
		t.Fatalf("ModelsURL() = %q, want %q", got, "https://api.deepseek.com/models")
	}
}
