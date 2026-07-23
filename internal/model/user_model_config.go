package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

type UserModelConfig struct {
	ID               uint64         `gorm:"primaryKey" json:"id"`
	UserID           uint64         `gorm:"not null;index" json:"user_id"`
	CredentialID     uint64         `gorm:"not null" json:"credential_id"`
	ProviderName     string         `gorm:"type:varchar(64);not null" json:"provider_name"`
	ModelName        string         `gorm:"type:varchar(128);not null" json:"model_name"`
	BaseURL          *string        `gorm:"type:varchar(512)" json:"base_url"`
	MaxContextTokens uint32         `gorm:"not null" json:"max_context_tokens"`
	MaxOutputTokens  uint32         `gorm:"not null" json:"max_output_tokens"`
	Settings         *JSONMap       `gorm:"type:json" json:"settings"`
	Revision         uint32         `gorm:"not null;default:1" json:"revision"`
	Enabled          bool           `gorm:"not null;default:true" json:"enabled"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`
}

func (UserModelConfig) TableName() string { return "user_model_config" }
func FindModelConfig(ctx context.Context, db *gorm.DB, userID, id uint64) (*UserModelConfig, error) {
	var v UserModelConfig
	return &v, db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&v).Error
}
func ListModelConfigs(ctx context.Context, db *gorm.DB, userID uint64) ([]UserModelConfig, error) {
	var v []UserModelConfig
	return v, db.WithContext(ctx).Where("user_id = ?", userID).Order("id DESC").Find(&v).Error
}
