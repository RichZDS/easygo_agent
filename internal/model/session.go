package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"easygo-agent/internal/platform/errorcode"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	SessionStatusNormal   int8 = 1
	SessionStatusArchived int8 = 2
	SessionStatusDisabled int8 = 3
)

// Session maps to table `session`.
type Session struct {
	ID               uint64         `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID           uint64         `gorm:"column:user_id;not null" json:"user_id"`
	Title            string         `gorm:"column:title;type:varchar(255);not null;default:新对话" json:"title"`
	Status           int8           `gorm:"column:status;type:tinyint;not null;default:1" json:"status"`
	CurrentAIModelID *uint64        `gorm:"column:current_ai_model_id" json:"current_ai_model_id"`
	ActiveRunID      *uint64        `gorm:"column:active_run_id" json:"active_run_id"`
	NextMessageSeq   uint64         `gorm:"column:next_message_seq;not null;default:1" json:"next_message_seq"`
	MessageCount     uint32         `gorm:"column:message_count;not null;default:0" json:"message_count"`
	LastMessageAt    *time.Time     `gorm:"column:last_message_at" json:"last_message_at"`
	CreatedAt        time.Time      `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time      `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt        gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}

func (Session) TableName() string { return "session" }

func CreateSession(ctx context.Context, db *gorm.DB, row *Session) error {
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("create session: %w", err))
	}
	return nil
}

func FindSessionOwnedByUser(ctx context.Context, db *gorm.DB, sessionID, userID uint64) (*Session, error) {
	var row Session
	if err := db.WithContext(ctx).Where("id = ? AND user_id = ?", sessionID, userID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorcode.New(errorcode.NotFound, "会话不存在")
		}
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find session: %w", err))
	}
	return &row, nil
}

func ListSessionsByUser(ctx context.Context, db *gorm.DB, userID uint64) ([]Session, error) {
	var rows []Session
	if err := db.WithContext(ctx).Where("user_id = ?", userID).
		Order("last_message_at DESC, id DESC").Find(&rows).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("list sessions: %w", err))
	}
	return rows, nil
}

func LockSession(ctx context.Context, db *gorm.DB, sessionID, userID uint64) (*Session, error) {
	var row Session
	if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND user_id = ?", sessionID, userID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorcode.New(errorcode.NotFound, "会话不存在")
		}
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("lock session: %w", err))
	}
	return &row, nil
}

func UpdateSessionModel(ctx context.Context, db *gorm.DB, sessionID, aiModelID uint64) error {
	result := db.WithContext(ctx).Model(&Session{}).Where("id = ?", sessionID).
		Update("current_ai_model_id", aiModelID)
	if result.Error != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("update session model: %w", result.Error))
	}
	if result.RowsAffected == 0 {
		return errorcode.New(errorcode.NotFound, "会话不存在")
	}
	return nil
}

func ClaimSessionRun(ctx context.Context, db *gorm.DB, sessionID, runID uint64) error {
	result := db.WithContext(ctx).Model(&Session{}).
		Where("id = ? AND active_run_id IS NULL", sessionID).
		Update("active_run_id", runID)
	if result.Error != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("claim session run: %w", result.Error))
	}
	if result.RowsAffected == 0 {
		return errorcode.New(errorcode.Conflict, "session already has an active run")
	}
	return nil
}

func ClearSessionActiveRun(ctx context.Context, db *gorm.DB, sessionID, runID uint64) error {
	result := db.WithContext(ctx).Model(&Session{}).
		Where("id = ? AND active_run_id = ?", sessionID, runID).
		Update("active_run_id", nil)
	if result.Error != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("clear active run: %w", result.Error))
	}
	return nil
}

func AllocateMessageSeq(ctx context.Context, db *gorm.DB, sessionID uint64) (uint64, error) {
	var row Session
	if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "next_message_seq").Where("id = ?", sessionID).First(&row).Error; err != nil {
		return 0, errorcode.Wrap(errorcode.Database, fmt.Errorf("allocate seq: %w", err))
	}
	seq := row.NextMessageSeq
	if err := db.WithContext(ctx).Model(&Session{}).Where("id = ?", sessionID).
		Update("next_message_seq", seq+1).Error; err != nil {
		return 0, errorcode.Wrap(errorcode.Database, fmt.Errorf("bump seq: %w", err))
	}
	return seq, nil
}

func BumpSessionMessageStats(ctx context.Context, db *gorm.DB, sessionID uint64, count int, at time.Time) error {
	result := db.WithContext(ctx).Model(&Session{}).Where("id = ?", sessionID).Updates(map[string]any{
		"message_count":   gorm.Expr("message_count + ?", count),
		"last_message_at": at,
	})
	if result.Error != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("bump session stats: %w", result.Error))
	}
	return nil
}
