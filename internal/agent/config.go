package agent

import (
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/eino-ext/components/model/agenticdeepseek"
	"github.com/cloudwego/eino-ext/components/model/deepseek"
)

const DefaultBaseURL = "https://api.deepseek.com"

type ResponseFormatType string

const (
	ResponseFormatTypeText       ResponseFormatType = "text"
	ResponseFormatTypeJSONObject ResponseFormatType = "json_object"
)

// 配置参数详情请查看：
// https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
type Config struct {
	// APIKey 是用于身份认证的密钥。
	// 必填。
	APIKey string

	// Timeout 指定等待 API 响应的最大时长。
	// 如果设置了 HTTPClient，则不会使用 Timeout。
	// 可选。
	Timeout time.Duration

	// HTTPClient 指定用于发送 HTTP 请求的客户端。
	// 如果设置了 HTTPClient，则不会使用 Timeout。
	// 可选。默认值：&http.Client{Timeout: Timeout}
	HTTPClient *http.Client

	// BaseURL 是自定义的 DeepSeek API 服务地址。
	// 可选。默认值：https://api.deepseek.com
	BaseURL string

	// Model 指定要使用的模型 ID。
	// 必填。
	Model string

	// MaxTokens 限制本次聊天补全最多生成的 Token 数量。
	// 可选。默认值请查看：
	// https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
	MaxTokens *int

	// Temperature 指定采样温度，用于控制模型输出的随机性。
	// 可选。默认值请查看：
	// https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
	Temperature *float32

	// TopP 通过核采样控制模型输出的多样性。
	// 可选。默认值请查看：
	// https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
	TopP *float32

	// Stop 指定停止序列。当模型生成这些内容时，将停止继续生成。
	// 可选。默认值请查看：
	// https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
	Stop []string

	// PresencePenalty 根据某个 Token 是否已经出现对其进行惩罚，
	// 用于减少内容重复并鼓励模型讨论新的主题。
	// 可选。默认值请查看：
	// https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
	PresencePenalty *float32

	// ResponseFormatType 指定模型响应内容的格式。
	// 可选。默认值请查看：
	// https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
	ResponseFormatType ResponseFormatType

	// FrequencyPenalty 根据某个 Token 已经出现的频率对其进行惩罚，
	// 用于减少模型重复生成相同内容。
	// 可选。默认值请查看：
	// https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
	FrequencyPenalty *float32

	// LogProbs 指定是否返回输出 Token 的对数概率。
	// 可选。默认值请查看：
	// https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
	LogProbs *bool

	// TopLogProbs 指定在每个 Token 位置返回概率最高的候选 Token 数量。
	// 可选。默认值请查看：
	// https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
	TopLogProbs *int
}

func (c Config) Validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("api key is required")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	return nil
}

// 转换为 agenticdeepseek 的配置
func (c Config) ToAgenticDeepSeekConfig() *agenticdeepseek.Config {
	cfg := &agenticdeepseek.Config{
		APIKey: c.APIKey,
		Model:  c.Model,
	}
	if c.Timeout > 0 {
		cfg.Timeout = c.Timeout
	}
	if c.HTTPClient != nil {
		cfg.HTTPClient = c.HTTPClient
	}
	if c.BaseURL != "" {
		cfg.BaseURL = c.BaseURL
	}
	if c.MaxTokens != nil {
		cfg.MaxTokens = c.MaxTokens
	}
	if c.Temperature != nil {
		cfg.Temperature = c.Temperature
	}
	if c.TopP != nil {
		cfg.TopP = c.TopP
	}
	if len(c.Stop) > 0 {
		cfg.Stop = c.Stop
	}
	if c.PresencePenalty != nil {
		cfg.PresencePenalty = c.PresencePenalty
	}
	if c.ResponseFormatType != "" {
		cfg.ResponseFormatType = agenticdeepseek.ResponseFormatType(c.ResponseFormatType)
	}
	if c.FrequencyPenalty != nil {
		cfg.FrequencyPenalty = c.FrequencyPenalty
	}
	if c.LogProbs != nil {
		cfg.LogProbs = c.LogProbs
	}
	if c.TopLogProbs != nil {
		cfg.TopLogProbs = c.TopLogProbs
	}
	return cfg
}

func (c Config) ToChatModelConfig() *deepseek.ChatModelConfig {
	cfg := &deepseek.ChatModelConfig{
		APIKey: c.APIKey,
		Model:  c.Model,
	}
	if c.Timeout > 0 {
		cfg.Timeout = c.Timeout
	}
	if c.HTTPClient != nil {
		cfg.HTTPClient = c.HTTPClient
	}
	if c.BaseURL != "" {
		cfg.BaseURL = c.BaseURL
	}
	if c.MaxTokens != nil {
		cfg.MaxTokens = *c.MaxTokens
	}
	if c.Temperature != nil {
		cfg.Temperature = *c.Temperature
	}
	if c.TopP != nil {
		cfg.TopP = *c.TopP
	}
	if len(c.Stop) > 0 {
		cfg.Stop = c.Stop
	}
	if c.PresencePenalty != nil {
		cfg.PresencePenalty = *c.PresencePenalty
	}
	if c.ResponseFormatType != "" {
		cfg.ResponseFormatType = deepseek.ResponseFormatType(c.ResponseFormatType)
	}
	if c.FrequencyPenalty != nil {
		cfg.FrequencyPenalty = *c.FrequencyPenalty
	}
	if c.LogProbs != nil {
		cfg.LogProbs = *c.LogProbs
	}
	if c.TopLogProbs != nil {
		cfg.TopLogProbs = *c.TopLogProbs
	}
	return cfg
}
