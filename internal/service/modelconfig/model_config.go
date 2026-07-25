package modelconfig

import (
	"context"
	"fmt"
	"net/url"

	"easygo-agent/internal/credential"
	"easygo-agent/internal/model"
	"gorm.io/gorm"
)

type Service struct {
	db     *gorm.DB
	cipher *credential.Cipher
}

func New(db *gorm.DB, cipher *credential.Cipher) *Service { return &Service{db: db, cipher: cipher} }
func aad(userID uint64, provider string) []byte {
	return []byte(fmt.Sprintf("user:%d:provider:%s", userID, provider))
}
func (s *Service) UpsertCredential(ctx context.Context, userID uint64, provider, apiKey string) (*model.UserProviderCredential, error) {
	if provider == "" || apiKey == "" {
		return nil, fmt.Errorf("provider and api_key are required")
	}
	sealed, err := s.cipher.Encrypt(apiKey, aad(userID, provider))
	if err != nil {
		return nil, err
	}
	v := model.UserProviderCredential{UserID: userID, ProviderName: provider, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, Algorithm: sealed.Algorithm, KeyVersion: sealed.KeyVersion, Status: model.CredentialStatusEnabled}
	err = s.db.WithContext(ctx).Where("user_id = ? AND provider_name = ?", userID, provider).Assign(v).FirstOrCreate(&v).Error
	return &v, err
}
func (s *Service) Create(ctx context.Context, userID, credentialID uint64, provider, modelName string, baseURL *string, contextTokens, outputTokens uint32, settings *model.JSONMap) (*model.UserModelConfig, error) {
	if provider != "deepseek" && provider != "openai" && provider != "minimax" {
		return nil, fmt.Errorf("unsupported provider")
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
