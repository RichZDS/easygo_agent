package chat

import (
	"context"
	"time"

	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/uuidgen"

	"gorm.io/gorm"
)

// ChatSessionService 定义聊天会话业务逻辑接口
type ChatSessionService interface {
	CreateSession(ctx context.Context, userID uint64, title string) (*model.ChatSession, error)
	FindByID(ctx context.Context, id uint64) (*model.ChatSession, error)
	FindBySessionID(ctx context.Context, sessionID string) (*model.ChatSession, error)
	ListByUserID(ctx context.Context, userID uint64) ([]model.ChatSession, error)
	UpdateTitle(ctx context.Context, id uint64, title string) error
	UpdateStatus(ctx context.Context, id uint64, status uint8) error
	RecordMessage(ctx context.Context, id uint64) error
	SoftDelete(ctx context.Context, id uint64) error
}

type chatSessionServiceImpl struct {
	db *gorm.DB
}

func NewChatSessionService(db *gorm.DB) ChatSessionService {
	return &chatSessionServiceImpl{db: db}
}

// CreateSession 创建会话
func (s *chatSessionServiceImpl) CreateSession(ctx context.Context, userID uint64, title string) (*model.ChatSession, error) {
	sessionID := uuidgen.New() // 生成对外唯一 session_id
	session := &model.ChatSession{
		SessionID: sessionID,
		UserID:    userID,
		Title:     title,
		Status:    model.ChatSessionStatusNormal,
	}
	if err := model.CreateChatSession(ctx, s.db, session); err != nil {
		return nil, err
	}
	return session, nil
}

// FindByID 根据主键 ID 查询
func (s *chatSessionServiceImpl) FindByID(ctx context.Context, id uint64) (*model.ChatSession, error) {
	return model.FindChatSessionByID(ctx, s.db, id)
}

// FindBySessionID 根据对外 session_id 查询
func (s *chatSessionServiceImpl) FindBySessionID(ctx context.Context, sessionID string) (*model.ChatSession, error) {
	return model.FindChatSessionBySessionID(ctx, s.db, sessionID)
}

// ListByUserID 查询某用户的所有会话
func (s *chatSessionServiceImpl) ListByUserID(ctx context.Context, userID uint64) ([]model.ChatSession, error) {
	return model.FindChatSessionsByUserID(ctx, s.db, userID)
}

// UpdateTitle 更新会话标题
func (s *chatSessionServiceImpl) UpdateTitle(ctx context.Context, id uint64, title string) error {
	return model.UpdateChatSessionTitle(ctx, s.db, id, title)
}

// UpdateStatus 更新会话状态（归档/禁用）
func (s *chatSessionServiceImpl) UpdateStatus(ctx context.Context, id uint64, status uint8) error {
	return model.UpdateChatSessionStatus(ctx, s.db, id, status)
}

// RecordMessage 记录消息（更新最后消息时间 + 消息计数+1）
func (s *chatSessionServiceImpl) RecordMessage(ctx context.Context, id uint64) error {
	return model.UpdateChatSessionLastMessage(ctx, s.db, id, time.Now())
}

// SoftDelete 软删除会话
func (s *chatSessionServiceImpl) SoftDelete(ctx context.Context, id uint64) error {
	return model.SoftDeleteChatSession(ctx, s.db, id)
}
