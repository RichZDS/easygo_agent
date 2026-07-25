package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

const (
	TurnPending   uint8 = 1
	TurnRunning   uint8 = 2
	TurnCompleted uint8 = 3
	TurnFailed    uint8 = 4
	TurnCancelled uint8 = 5
)

type ChatTurn struct {
	ID            uint64     `gorm:"primaryKey" json:"id"`
	TurnID        string     `gorm:"type:varchar(64);uniqueIndex" json:"turn_id"`
	UserID        uint64     `gorm:"index;not null" json:"user_id"`
	ChatSessionID uint64     `gorm:"index;not null" json:"chat_session_id"`
	ModelConfigID uint64     `gorm:"not null" json:"model_config_id"`
	ModelRevision uint32     `gorm:"not null" json:"model_revision"`
	RequestID     string     `gorm:"type:varchar(64);not null" json:"request_id"`
	Input         string     `gorm:"type:mediumtext;not null" json:"-"`
	Status        uint8      `gorm:"not null" json:"status"`
	StreamID      *string    `gorm:"type:varchar(64)" json:"-"`
	ErrorCode     *string    `gorm:"type:varchar(64)" json:"error_code,omitempty"`
	ErrorMessage  *string    `gorm:"type:varchar(1000)" json:"error_message,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (ChatTurn) TableName() string { return "chat_turn" }
func FindTurn(ctx context.Context, db *gorm.DB, userID uint64, turnID string) (*ChatTurn, error) {
	var v ChatTurn
	return &v, db.WithContext(ctx).Where("turn_id = ? AND user_id = ?", turnID, userID).First(&v).Error
}
func FindTurnInternal(ctx context.Context, db *gorm.DB, turnID string) (*ChatTurn, error) {
	var v ChatTurn
	return &v, db.WithContext(ctx).Where("turn_id = ?", turnID).First(&v).Error
}
func FindOpenTurnForSession(ctx context.Context, db *gorm.DB, sessionID uint64) (*ChatTurn, error) {
	var v ChatTurn
	err := db.WithContext(ctx).Where("chat_session_id = ? AND status IN ?", sessionID, []int{int(TurnPending), int(TurnRunning)}).First(&v).Error
	return &v, err
}
