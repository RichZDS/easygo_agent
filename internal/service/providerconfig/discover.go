package providerconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	agentframework "easygo-agent/internal/agent"
	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/platform/logger"

	"go.uber.org/zap"
)

// RemoteModel 表示上游 Provider 返回的可用模型条目。
type RemoteModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DiscoverModelsInput 描述探测上游模型列表所需的连接参数。
type DiscoverModelsInput struct {
	Type    string
	APIKey  string
	BaseURL string
}

type openAIModelsResponse struct {
	Data []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"data"`
}

// DiscoverModels 调用上游 OpenAI-compatible /models 接口并返回可用模型列表。
func (s *Service) DiscoverModels(ctx context.Context, in DiscoverModelsInput) ([]RemoteModel, error) {
	providerType := strings.ToLower(strings.TrimSpace(in.Type))
	apiKey := strings.TrimSpace(in.APIKey)
	if providerType == "" || apiKey == "" {
		err := errorcode.New(errorcode.InvalidParameter, "type and api_key are required")
		logger.ErrorContext(ctx, "discover provider models failed", zap.String("provider", providerType), zap.Error(err))
		return nil, err
	}
	if !s.runtime.Supports(providerType) {
		err := errorcode.New(errorcode.InvalidParameter, fmt.Sprintf("provider %q is not supported", providerType))
		logger.ErrorContext(ctx, "discover provider models failed", zap.String("provider", providerType), zap.Error(err))
		return nil, err
	}

	baseURL := strings.TrimSpace(in.BaseURL)
	if baseURL == "" {
		baseURL = agentframework.DefaultBaseURL(providerType)
	}
	if baseURL == "" {
		err := errorcode.New(errorcode.InvalidParameter, "base_url is required")
		logger.ErrorContext(ctx, "discover provider models failed", zap.String("provider", providerType), zap.Error(err))
		return nil, err
	}

	modelsURL := agentframework.ModelsURL(baseURL)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		logger.ErrorContext(ctx, "discover provider models failed", zap.String("provider", providerType), zap.String("models_url", modelsURL), zap.Error(err))
		return nil, errorcode.Wrap(errorcode.Internal, fmt.Errorf("build models request: %w", err))
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		logger.ErrorContext(ctx, "discover provider models failed", zap.String("provider", providerType), zap.String("models_url", modelsURL), zap.Error(err))
		return nil, errorcode.Wrap(errorcode.Internal, fmt.Errorf("request provider models: %w", err))
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		logger.ErrorContext(ctx, "discover provider models failed", zap.String("provider", providerType), zap.Int("status_code", response.StatusCode), zap.Error(err))
		return nil, errorcode.Wrap(errorcode.Internal, fmt.Errorf("read provider models response: %w", err))
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		err := errorcode.New(errorcode.InvalidParameter, fmt.Sprintf("provider models request failed with status %d", response.StatusCode))
		logger.ErrorContext(ctx, "discover provider models failed", zap.String("provider", providerType), zap.Int("status_code", response.StatusCode), zap.Error(err))
		return nil, err
	}

	var payload openAIModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.ErrorContext(ctx, "discover provider models failed", zap.String("provider", providerType), zap.Error(err))
		return nil, errorcode.Wrap(errorcode.Internal, fmt.Errorf("decode provider models response: %w", err))
	}

	models := make([]RemoteModel, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = id
		}
		models = append(models, RemoteModel{ID: id, Name: name})
	}
	if len(models) == 0 {
		err := errorcode.New(errorcode.InvalidParameter, "provider returned no models")
		logger.ErrorContext(ctx, "discover provider models failed", zap.String("provider", providerType), zap.Error(err))
		return nil, err
	}
	return models, nil
}
