package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

const CredentialStatusEnabled uint8 = 1

type UserProviderCredential struct {
	ID           uint64         `gorm:"primaryKey" json:"id"`
	UserID       uint64         `gorm:"not null;index" json:"user_id"`
	ProviderName string         `gorm:"type:varchar(64);not null" json:"provider_name"`
	Ciphertext   string         `gorm:"type:text;not null" json:"-"`
	Nonce        string         `gorm:"type:varchar(32);not null" json:"-"`
	Algorithm    string         `gorm:"type:varchar(32);not null" json:"-"`
	KeyVersion   string         `gorm:"type:varchar(32);not null" json:"-"`
	Status       uint8          `gorm:"not null;default:1" json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

func (UserProviderCredential) TableName() string { return "user_provider_credential" }
func FindCredential(ctx context.Context, db *gorm.DB, userID, id uint64) (*UserProviderCredential, error) {
	var v UserProviderCredential
	return &v, db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&v).Error
}
func FindCredentialByProvider(ctx context.Context, db *gorm.DB, userID uint64, provider string) (*UserProviderCredential, error) {
	var v UserProviderCredential
	return &v, db.WithContext(ctx).Where("user_id = ? AND provider_name = ?", userID, provider).First(&v).Error
}
