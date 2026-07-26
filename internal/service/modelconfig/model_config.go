package modelconfig

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	agentframework "easygo-agent/internal/agent"
	"easygo-agent/internal/credential"
	"easygo-agent/internal/model"
	"gorm.io/gorm"
)

type Service struct {
	db      *gorm.DB
	cipher  *credential.Cipher
	runtime *agentframework.RuntimeFactory
}

func New(db *gorm.DB, cipher *credential.Cipher, runtime *agentframework.RuntimeFactory) *Service {
	return &Service{db: db, cipher: cipher, runtime: runtime}
}
func (s *Service) UpsertCredential(ctx context.Context, userID uint64, provider, apiKey string) (*model.UserProviderCredential, error) {
	if provider == "" || apiKey == "" {
		return nil, fmt.Errorf("provider and api_key are required")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !s.runtime.Supports(provider) {
		return nil, fmt.Errorf("provider %q has no registered Eino adapter", provider)
	}
	sealed, err := s.cipher.Encrypt(apiKey, credential.AssociatedData(userID, provider))
	if err != nil {
		return nil, err
	}
	v := model.UserProviderCredential{UserID: userID, ProviderName: provider, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, Algorithm: sealed.Algorithm, KeyVersion: sealed.KeyVersion, Status: model.CredentialStatusEnabled}
	err = s.db.WithContext(ctx).Where("user_id = ? AND provider_name = ?", userID, provider).Assign(v).FirstOrCreate(&v).Error
	return &v, err
}
func (s *Service) Create(ctx context.Context, userID, credentialID uint64, provider, modelName string, baseURL *string, contextTokens, outputTokens uint32, settings *model.JSONMap) (*model.UserModelConfig, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !s.runtime.Supports(provider) {
		return nil, fmt.Errorf("provider %q has no registered Eino adapter", provider)
	}
	if modelName == "" || contextTokens == 0 || outputTokens == 0 || outputTokens >= contextTokens {
		return nil, fmt.Errorf("invalid model token limits")
	}
	if baseURL != nil {
		u, err := url.ParseRequestURI(*baseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("base_url must be an absolute HTTP(S) URL")
		}
	}
	if provider == agentframework.ProviderMiniMax && (baseURL == nil || strings.TrimSpace(*baseURL) == "") {
		return nil, fmt.Errorf("minimax requires an OpenAI-compatible base_url")
	}
	credential, err := model.FindCredential(ctx, s.db, userID, credentialID)
	if err != nil {
		return nil, err
	}
	if credential.ProviderName != provider || credential.Status != model.CredentialStatusEnabled {
		return nil, fmt.Errorf("credential does not match enabled provider")
	}
	v := &model.UserModelConfig{UserID: userID, CredentialID: credentialID, ProviderName: provider, ModelName: modelName, BaseURL: baseURL, MaxContextTokens: contextTokens, MaxOutputTokens: outputTokens, Settings: settings, Revision: 1, Enabled: true}
	return v, s.db.WithContext(ctx).Create(v).Error
}
func (s *Service) List(ctx context.Context, userID uint64) ([]model.UserModelConfig, error) {
	return model.ListModelConfigs(ctx, s.db, userID)
}
