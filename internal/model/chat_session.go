package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"easygo-agent/internal/errorcode"

	"gorm.io/gorm"
)

// ChatSession maps to the MySQL `chat_session` table.
type ChatSession struct {
	ID            uint64         `gorm:"column:id;type:bigint unsigned;primaryKey;autoIncrement" json:"id"`
	SessionID     string         `gorm:"column:session_id;type:varchar(64);not null;uniqueIndex:uk_session_id" json:"session_id"`
	UserID        uint64         `gorm:"column:user_id;type:bigint unsigned;not null;index:idx_user_id;index:idx_user_last_message,priority:1;index:idx_user_deleted,priority:1" json:"user_id"`
	Title         string         `gorm:"column:title;type:varchar(255);not null;default:新对话" json:"title"`
	Status        uint8          `gorm:"column:status;type:tinyint unsigned;not null;default:1" json:"status"`
	LastMessageAt *time.Time     `gorm:"column:last_message_at" json:"last_message_at"`
	MessageCount  uint32         `gorm:"column:message_count;type:int unsigned;not null;default:0" json:"message_count"`
	CreatedAt     time.Time      `gorm:"column:created_at;not null;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time      `gorm:"column:updated_at;not null;autoUpdateTime" json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"column:deleted_at;index:idx_user_deleted,priority:2" json:"-"`
}

func (ChatSession) TableName() string { return "chat_session" }

// ========== 会话状态常量 ==========

const (
	ChatSessionStatusNormal   uint8 = 1 // 正常
	ChatSessionStatusArchived uint8 = 2 // 归档
	ChatSessionStatusDisabled uint8 = 3 // 禁用
)

// ========== GORM 包级查询函数 ==========

// CreateChatSession 创建会话
func CreateChatSession(ctx context.Context, db *gorm.DB, session *ChatSession) error {
	if err := db.WithContext(ctx).Create(session).Error; err != nil {
		return mapChatSessionWriteError("create chat session", err)
	}
	return nil
}

// FindChatSessionByID 根据主键 ID 查询
func FindChatSessionByID(ctx context.Context, db *gorm.DB, id uint64) (*ChatSession, error) {
	return findOneChatSession(db.WithContext(ctx).Where("id = ?", id))
}

// FindChatSessionBySessionID 根据对外 session_id 查询
func FindChatSessionBySessionID(ctx context.Context, db *gorm.DB, sessionID string) (*ChatSession, error) {
	return findOneChatSession(db.WithContext(ctx).Where("session_id = ?", sessionID))
}

// FindChatSessionsByUserID 查询某用户的所有会话，按 last_message_at 倒序
func FindChatSessionsByUserID(ctx context.Context, db *gorm.DB, userID uint64) ([]ChatSession, error) {
	var sessions []ChatSession
	if err := db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("last_message_at DESC").
		Find(&sessions).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find chat sessions by user: %w", err))
	}
	return sessions, nil
}

// UpdateChatSessionTitle 更新会话标题
func UpdateChatSessionTitle(ctx context.Context, db *gorm.DB, id uint64, title string) error {
	result := db.WithContext(ctx).Model(&ChatSession{}).Where("id = ?", id).Update("title", title)
	return checkChatSessionUpdate("update chat session title", result)
}

// UpdateChatSessionStatus 更新会话状态
func UpdateChatSessionStatus(ctx context.Context, db *gorm.DB, id uint64, status uint8) error {
	result := db.WithContext(ctx).Model(&ChatSession{}).Where("id = ?", id).Update("status", status)
	return checkChatSessionUpdate("update chat session status", result)
}

// UpdateChatSessionLastMessage 更新最后消息时间和消息计数
func UpdateChatSessionLastMessage(ctx context.Context, db *gorm.DB, id uint64, msgTime time.Time) error {
	result := db.WithContext(ctx).Model(&ChatSession{}).Where("id = ?", id).Updates(map[string]any{
		"last_message_at": msgTime,
		"message_count":   gorm.Expr("message_count + 1"),
	})
	return checkChatSessionUpdate("update chat session last message", result)
}

// SoftDeleteChatSession 软删除会话
func SoftDeleteChatSession(ctx context.Context, db *gorm.DB, id uint64) error {
	result := db.WithContext(ctx).Delete(&ChatSession{}, id)
	return checkChatSessionUpdate("delete chat session", result)
}

// ========== 内部辅助函数 ==========

func findOneChatSession(query *gorm.DB) (*ChatSession, error) {
	var session ChatSession
	if err := query.First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorcode.New(errorcode.NotFound, "会话不存在")
		}
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find chat session: %w", err))
	}
	return &session, nil
}

func checkChatSessionUpdate(operation string, result *gorm.DB) error {
	if result.Error != nil {
		return mapChatSessionWriteError(operation, result.Error)
	}
	if result.RowsAffected == 0 {
		return errorcode.New(errorcode.NotFound, "会话不存在")
	}
	return nil
}

func mapChatSessionWriteError(operation string, err error) error {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return errorcode.New(errorcode.Conflict, "会话已存在")
	}
	return errorcode.Wrap(errorcode.Database, fmt.Errorf("%s: %w", operation, err))
}
