package model

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"easygo-agent/internal/platform/errorcode"

	"gorm.io/gorm"
)

// JSONMap stores arbitrary JSON objects.
type JSONMap map[string]any

func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (m *JSONMap) Scan(value any) error {
	if value == nil {
		*m = nil
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported JSONMap type %T", value)
	}
	if len(raw) == 0 {
		*m = nil
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	*m = out
	return nil
}

// EncryptedAPIKey is stored in provider.api_key as JSON.
type EncryptedAPIKey struct {
	Ciphertext string `json:"ciphertext"`
	Nonce      string `json:"nonce"`
	Algorithm  string `json:"algorithm"`
	KeyVersion string `json:"key_version"`
}

func (k EncryptedAPIKey) Value() (driver.Value, error) {
	b, err := json.Marshal(k)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (k *EncryptedAPIKey) Scan(value any) error {
	if value == nil {
		*k = EncryptedAPIKey{}
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported EncryptedAPIKey type %T", value)
	}
	return json.Unmarshal(raw, k)
}

const (
	StatusEnabled  uint8 = 1
	StatusDisabled uint8 = 2
)

// Provider maps to table `provider`.
type Provider struct {
	ID        uint64          `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID    uint64          `gorm:"column:user_id;not null;uniqueIndex:uk_user_type,priority:1" json:"user_id"`
	Name      string          `gorm:"column:name;type:varchar(128);not null" json:"name"`
	Type      string          `gorm:"column:type;type:varchar(64);not null;uniqueIndex:uk_user_type,priority:2" json:"type"`
	APIKey    EncryptedAPIKey `gorm:"column:api_key;type:text;not null" json:"-"`
	BaseURL   *string         `gorm:"column:base_url;type:varchar(512)" json:"base_url"`
	TestModel string          `gorm:"column:test_model;type:varchar(128);not null;default:''" json:"test_model"`
	Status    uint8           `gorm:"column:status;type:tinyint unsigned;not null;default:1" json:"status"`
	CreatedAt time.Time       `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time       `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (Provider) TableName() string { return "provider" }

func CreateProvider(ctx context.Context, db *gorm.DB, row *Provider) error {
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return errorcode.New(errorcode.Conflict, "provider already exists")
		}
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("create provider: %w", err))
	}
	return nil
}

func UpsertProvider(ctx context.Context, db *gorm.DB, row *Provider) error {
	var existing Provider
	err := db.WithContext(ctx).Where("user_id = ? AND type = ?", row.UserID, row.Type).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CreateProvider(ctx, db, row)
	}
	if err != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("find provider: %w", err))
	}
	row.ID = existing.ID
	row.CreatedAt = existing.CreatedAt
	result := db.WithContext(ctx).Model(&Provider{}).Where("id = ?", existing.ID).Updates(map[string]any{
		"name":       row.Name,
		"api_key":    row.APIKey,
		"base_url":   row.BaseURL,
		"test_model": row.TestModel,
		"status":     row.Status,
	})
	if result.Error != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("update provider: %w", result.Error))
	}
	return nil
}

func FindProviderByID(ctx context.Context, db *gorm.DB, userID, id uint64) (*Provider, error) {
	var row Provider
	if err := db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorcode.New(errorcode.NotFound, "provider 不存在")
		}
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find provider: %w", err))
	}
	return &row, nil
}

func ListProvidersByUser(ctx context.Context, db *gorm.DB, userID uint64) ([]Provider, error) {
	var rows []Provider
	if err := db.WithContext(ctx).Where("user_id = ?", userID).Order("id DESC").Find(&rows).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("list providers: %w", err))
	}
	return rows, nil
}

// AIModel maps to table `ai_model`.
type AIModel struct {
	ID         uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID     uint64    `gorm:"column:user_id;not null" json:"user_id"`
	ProviderID uint64    `gorm:"column:provider_id;not null;uniqueIndex:uk_provider_model,priority:1" json:"provider_id"`
	Name       string    `gorm:"column:name;type:varchar(128);not null" json:"name"`
	ModelID    string    `gorm:"column:model_id;type:varchar(128);not null;uniqueIndex:uk_provider_model,priority:2" json:"model_id"`
	Params     JSONMap   `gorm:"column:params;type:json" json:"params"`
	Status     uint8     `gorm:"column:status;type:tinyint unsigned;not null;default:1" json:"status"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (AIModel) TableName() string { return "ai_model" }

func CreateAIModel(ctx context.Context, db *gorm.DB, row *AIModel) error {
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return errorcode.New(errorcode.Conflict, "ai model already exists")
		}
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("create ai model: %w", err))
	}
	return nil
}

func FindAIModelByID(ctx context.Context, db *gorm.DB, userID, id uint64) (*AIModel, error) {
	var row AIModel
	if err := db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorcode.New(errorcode.NotFound, "ai model 不存在")
		}
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find ai model: %w", err))
	}
	return &row, nil
}

func ListAIModelsByUser(ctx context.Context, db *gorm.DB, userID uint64) ([]AIModel, error) {
	var rows []AIModel
	if err := db.WithContext(ctx).Where("user_id = ? AND status = ?", userID, StatusEnabled).
		Order("id DESC").Find(&rows).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("list ai models: %w", err))
	}
	return rows, nil
}
