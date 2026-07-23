package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

type ChatSummary struct {
	ID             uint64    `gorm:"primaryKey" json:"id"`
	ChatSessionID  uint64    `gorm:"not null;index" json:"chat_session_id"`
	FromSequenceNo uint32    `gorm:"not null" json:"from_sequence_no"`
	ToSequenceNo   uint32    `gorm:"not null" json:"to_sequence_no"`
	Content        string    `gorm:"type:mediumtext;not null" json:"content"`
	TokenCount     uint32    `gorm:"not null" json:"token_count"`
	ModelConfigID  uint64    `gorm:"not null" json:"model_config_id"`
	ModelRevision  uint32    `gorm:"not null" json:"model_revision"`
	CreatedAt      time.Time `json:"created_at"`
}

func (ChatSummary) TableName() string { return "chat_summary" }
func FindLatestChatSummary(ctx context.Context, db *gorm.DB, sessionID uint64) (*ChatSummary, error) {
	var v ChatSummary
	return &v, db.WithContext(ctx).Where("chat_session_id = ?", sessionID).Order("to_sequence_no DESC").First(&v).Error
}
