package providerconfig

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	agentframework "easygo-agent/internal/agent"
	"easygo-agent/internal/credential"
	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/errorcode"

	"gorm.io/gorm"
)

// Service manages provider credentials and ai_model rows.
type Service struct {
	db      *gorm.DB
	cipher  *credential.Cipher
	runtime *agentframework.RuntimeFactory
}

func New(db *gorm.DB, cipher *credential.Cipher, runtime *agentframework.RuntimeFactory) *Service {
	return &Service{db: db, cipher: cipher, runtime: runtime}
}

type UpsertProviderInput struct {
	Name      string
	Type      string
	APIKey    string
	BaseURL   *string
	TestModel string
}

func (s *Service) UpsertProvider(ctx context.Context, userID uint64, in UpsertProviderInput) (*model.Provider, error) {
	providerType := strings.ToLower(strings.TrimSpace(in.Type))
	if providerType == "" || strings.TrimSpace(in.APIKey) == "" {
		return nil, errorcode.New(errorcode.InvalidParameter, "type and api_key are required")
	}
	if !s.runtime.Supports(providerType) {
		return nil, errorcode.New(errorcode.InvalidParameter, fmt.Sprintf("provider %q is not supported", providerType))
	}
	if in.BaseURL != nil {
		u, err := url.ParseRequestURI(*in.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errorcode.New(errorcode.InvalidParameter, "base_url must be an absolute HTTP(S) URL")
		}
	}
	if providerType == agentframework.ProviderMiniMax && (in.BaseURL == nil || strings.TrimSpace(*in.BaseURL) == "") {
		return nil, errorcode.New(errorcode.InvalidParameter, "minimax requires an OpenAI-compatible base_url")
	}
	sealed, err := s.cipher.Encrypt(in.APIKey, credential.AssociatedData(userID, providerType))
	if err != nil {
		return nil, errorcode.Wrap(errorcode.Internal, err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = providerType
	}
	row := &model.Provider{
		UserID: userID,
		Name:   name,
		Type:   providerType,
		APIKey: model.EncryptedAPIKey{
			Ciphertext: sealed.Ciphertext,
			Nonce:      sealed.Nonce,
			Algorithm:  sealed.Algorithm,
			KeyVersion: sealed.KeyVersion,
		},
		BaseURL:   in.BaseURL,
		TestModel: strings.TrimSpace(in.TestModel),
		Status:    model.StatusEnabled,
	}
	if err := model.UpsertProvider(ctx, s.db, row); err != nil {
		return nil, err
	}
	saved, err := model.FindProviderByID(ctx, s.db, userID, row.ID)
	if err != nil {
		// After insert FirstOrCreate style upsert may leave ID on row.
		if row.ID != 0 {
			return row, nil
		}
		return nil, err
	}
	return saved, nil
}

func (s *Service) ListProviders(ctx context.Context, userID uint64) ([]model.Provider, error) {
	return model.ListProvidersByUser(ctx, s.db, userID)
}

type CreateAIModelInput struct {
	ProviderID uint64
	Name       string
	ModelID    string
	Params     model.JSONMap
}

func (s *Service) CreateAIModel(ctx context.Context, userID uint64, in CreateAIModelInput) (*model.AIModel, error) {
	provider, err := model.FindProviderByID(ctx, s.db, userID, in.ProviderID)
	if err != nil {
		return nil, err
	}
	if provider.Status != model.StatusEnabled {
		return nil, errorcode.New(errorcode.InvalidParameter, "provider is disabled")
	}
	name := strings.TrimSpace(in.Name)
	modelID := strings.TrimSpace(in.ModelID)
	if name == "" || modelID == "" {
		return nil, errorcode.New(errorcode.InvalidParameter, "name and model_id are required")
	}
	row := &model.AIModel{
		UserID:     userID,
		ProviderID: provider.ID,
		Name:       name,
		ModelID:    modelID,
		Params:     in.Params,
		Status:     model.StatusEnabled,
	}
	if err := model.CreateAIModel(ctx, s.db, row); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *Service) ListAIModels(ctx context.Context, userID uint64) ([]model.AIModel, error) {
	return model.ListAIModelsByUser(ctx, s.db, userID)
}

// ResolveModelSpec decrypts provider key and builds a ModelSpec for runtime.
func (s *Service) ResolveModelSpec(ctx context.Context, userID, aiModelID uint64) (agentframework.ModelSpec, *model.AIModel, *model.Provider, error) {
	aiModel, err := model.FindAIModelByID(ctx, s.db, userID, aiModelID)
	if err != nil {
		return agentframework.ModelSpec{}, nil, nil, err
	}
	if aiModel.Status != model.StatusEnabled {
		return agentframework.ModelSpec{}, nil, nil, errorcode.New(errorcode.InvalidParameter, "ai model is disabled")
	}
	provider, err := model.FindProviderByID(ctx, s.db, userID, aiModel.ProviderID)
	if err != nil {
		return agentframework.ModelSpec{}, nil, nil, err
	}
	if provider.Status != model.StatusEnabled {
		return agentframework.ModelSpec{}, nil, nil, errorcode.New(errorcode.InvalidParameter, "provider is disabled")
	}
	apiKey, err := s.cipher.Decrypt(credential.Ciphertext{
		Ciphertext: provider.APIKey.Ciphertext,
		Nonce:      provider.APIKey.Nonce,
		Algorithm:  provider.APIKey.Algorithm,
		KeyVersion: provider.APIKey.KeyVersion,
	}, credential.AssociatedData(userID, provider.Type))
	if err != nil {
		return agentframework.ModelSpec{}, nil, nil, errorcode.New(errorcode.Internal, "provider api_key cannot be decrypted")
	}
	baseURL := ""
	if provider.BaseURL != nil {
		baseURL = *provider.BaseURL
	}
	settings := map[string]any(nil)
	if aiModel.Params != nil {
		settings = map[string]any(aiModel.Params)
	}
	maxOutput := uint32(0)
	if v, ok := settings["max_output_tokens"]; ok {
		switch n := v.(type) {
		case float64:
			if n > 0 {
				maxOutput = uint32(n)
			}
		case int:
			if n > 0 {
				maxOutput = uint32(n)
			}
		}
	}
	return agentframework.ModelSpec{
		Provider:        provider.Type,
		APIKey:          apiKey,
		Model:           aiModel.ModelID,
		BaseURL:         baseURL,
		MaxOutputTokens: maxOutput,
		Settings:        settings,
	}, aiModel, provider, nil
}
