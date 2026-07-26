package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"easygo-agent/internal/platform/errorcode"

	"gorm.io/gorm"
)

const (
	AgentRunRunning   uint8 = 1
	AgentRunSucceeded uint8 = 2
	AgentRunFailed    uint8 = 3
	AgentRunCancelled uint8 = 4
)

// AgentRun maps to table `agent_run`.
type AgentRun struct {
	ID               uint64     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	SessionID        uint64     `gorm:"column:session_id;not null" json:"session_id"`
	UserID           uint64     `gorm:"column:user_id;not null;uniqueIndex:uk_user_request,priority:1" json:"user_id"`
	RequestID        string     `gorm:"column:request_id;type:varchar(64);not null;uniqueIndex:uk_user_request,priority:2" json:"request_id"`
	AIModelID        uint64     `gorm:"column:ai_model_id;not null" json:"ai_model_id"`
	Status           uint8      `gorm:"column:status;type:tinyint unsigned;not null" json:"status"`
	PromptTokens     *uint32    `gorm:"column:prompt_tokens" json:"prompt_tokens"`
	CompletionTokens *uint32    `gorm:"column:completion_tokens" json:"completion_tokens"`
	TotalTokens      *uint32    `gorm:"column:total_tokens" json:"total_tokens"`
	LatencyMs        *uint32    `gorm:"column:latency_ms" json:"latency_ms"`
	ErrorCode        string     `gorm:"column:error_code;type:varchar(64)" json:"error_code"`
	ErrorMessage     string     `gorm:"column:error_message;type:varchar(1000)" json:"error_message"`
	ConfigSnapshot   JSONMap    `gorm:"column:config_snapshot;type:json" json:"config_snapshot"`
	StartedAt        *time.Time `gorm:"column:started_at" json:"started_at"`
	FinishedAt       *time.Time `gorm:"column:finished_at" json:"finished_at"`
	CreatedAt        time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
}

func (AgentRun) TableName() string { return "agent_run" }

func CreateAgentRun(ctx context.Context, db *gorm.DB, row *AgentRun) error {
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return errorcode.New(errorcode.Conflict, "duplicate request")
		}
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("create agent run: %w", err))
	}
	return nil
}

func FindAgentRunByUserRequest(ctx context.Context, db *gorm.DB, userID uint64, requestID string) (*AgentRun, error) {
	var row AgentRun
	if err := db.WithContext(ctx).Where("user_id = ? AND request_id = ?", userID, requestID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func FindAgentRunOwnedByUser(ctx context.Context, db *gorm.DB, userID, runID uint64) (*AgentRun, error) {
	var row AgentRun
	if err := db.WithContext(ctx).Where("id = ? AND user_id = ?", runID, userID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorcode.New(errorcode.NotFound, "run 不存在")
		}
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find agent run: %w", err))
	}
	return &row, nil
}

func FinalizeAgentRun(ctx context.Context, db *gorm.DB, runID uint64, updates map[string]any) error {
	result := db.WithContext(ctx).Model(&AgentRun{}).Where("id = ? AND status = ?", runID, AgentRunRunning).Updates(updates)
	if result.Error != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("finalize agent run: %w", result.Error))
	}
	return nil
}
