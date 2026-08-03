package agent

import "strings"

// DefaultBaseURL 返回各 Provider 的官方 OpenAI-compatible API 基址。
func DefaultBaseURL(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderDeepSeek:
		return "https://api.deepseek.com"
	case ProviderOpenAI:
		return "https://api.openai.com/v1"
	case ProviderMiniMax:
		return "https://api.minimaxi.com/v1"
	default:
		return ""
	}
}

// ModelsURL 根据 Provider 基址构造上游模型列表地址。
func ModelsURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/models"
}
