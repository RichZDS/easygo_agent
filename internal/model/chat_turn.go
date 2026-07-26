package model

import (
	"context"
	"errors"
	"time"

	"easygo-agent/internal/platform/errorcode"

	"gorm.io/gorm"
)

const (
	TurnPending   uint8 = 1
	TurnRunning   uint8 = 2
	TurnCompleted uint8 = 3
	TurnFailed    uint8 = 4
	TurnCancelled uint8 = 5
)

type ChatTurn struct {
	ID            uint64     `gorm:"column:id;primaryKey" json:"id"`
	TurnID        string     `gorm:"column:turn_id;type:varchar(64);uniqueIndex" json:"turn_id"`
	UserID        uint64     `gorm:"column:user_id;index;not null" json:"user_id"`
	ChatSessionID uint64     `gorm:"column:chat_session_id;index;not null;uniqueIndex:uk_turn_active,priority:1" json:"chat_session_id"`
	ModelConfigID uint64     `gorm:"column:model_config_id;not null" json:"model_config_id"`
	ModelRevision uint32     `gorm:"column:model_revision;not null" json:"model_revision"`
	AgentRevision string     `gorm:"column:agent_revision;type:varchar(64);not null" json:"agent_revision"`
	RequestID     string     `gorm:"column:request_id;type:varchar(64);not null" json:"request_id"`
	Status        uint8      `gorm:"column:status;not null" json:"status"`
	ActiveSlot    *uint8     `gorm:"column:active_slot;type:tinyint unsigned;uniqueIndex:uk_turn_active,priority:2" json:"-"`
	ErrorCode     *string    `gorm:"column:error_code;type:varchar(64)" json:"error_code,omitempty"`
	ErrorMessage  *string    `gorm:"column:error_message;type:varchar(1000)" json:"error_message,omitempty"`
	CreatedAt     time.Time  `gorm:"column:created_at" json:"created_at"`
	StartedAt     *time.Time `gorm:"column:started_at" json:"started_at,omitempty"`
	ExpiresAt     *time.Time `gorm:"column:expires_at" json:"expires_at,omitempty"`
	CompletedAt   *time.Time `gorm:"column:completed_at" json:"completed_at,omitempty"`
	UpdatedAt     time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (ChatTurn) TableName() string { return "chat_turn" }

func FindTurn(ctx context.Context, db *gorm.DB, userID uint64, turnID string) (*ChatTurn, error) {
	var turn ChatTurn
	if err := db.WithContext(ctx).
		Where("turn_id = ? AND user_id = ?", turnID, userID).
		First(&turn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorcode.New(errorcode.NotFound, "turn not found")
		}
		return nil, errorcode.Wrap(errorcode.Database, err)
	}
	return &turn, nil
}

func FindTurnByRequest(ctx context.Context, db *gorm.DB, sessionID uint64, requestID string) (*ChatTurn, error) {
	var turn ChatTurn
	err := db.WithContext(ctx).
		Where("chat_session_id = ? AND request_id = ?", sessionID, requestID).
		First(&turn).Error
	return &turn, err
}
