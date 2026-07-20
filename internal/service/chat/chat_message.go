package chat

import (
	"context"
	"time"

	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/uuidgen"

	"gorm.io/gorm"
)

// ChatMessageService 定义聊天消息业务逻辑接口
type ChatMessageService interface {
	CreateMessage(ctx context.Context, p *CreateMessageParams) (*model.ChatMessage, error)
	FindByID(ctx context.Context, id uint64) (*model.ChatMessage, error)
	FindByMessageID(ctx context.Context, messageID string) (*model.ChatMessage, error)
	ListBySessionID(ctx context.Context, sessionID uint64, offset, limit int) ([]model.ChatMessage, error)
	ListByTurnID(ctx context.Context, sessionID uint64, turnID string) ([]model.ChatMessage, error)
	UpdateStatus(ctx context.Context, id uint64, status uint8, finishReason *string) error
	UpdateContent(ctx context.Context, id uint64, content string) error
	UpdateTokens(ctx context.Context, id uint64, prompt, completion, total uint32) error
	SetError(ctx context.Context, id uint64, errorCode, errorMsg string) error
	SoftDelete(ctx context.Context, id uint64) error
	CreateToolResult(ctx context.Context, sessionID uint64, turnID, toolCallID, toolName, content string, requestID *string) (*model.ChatMessage, error)
	RecordMessageAndUpdate(ctx context.Context, p *CreateMessageParams) (*model.ChatMessage, error)
}

type chatMessageServiceImpl struct {
	db *gorm.DB
}

func NewChatMessageService(db *gorm.DB) ChatMessageService {
	return &chatMessageServiceImpl{db: db}
}

// ========== CreateMessage 参数 ==========

type CreateMessageParams struct {
	ChatSessionID   uint64
	TurnID          *string
	ParentMessageID *string
	Role            uint8
	MessageType     uint8
	Content         *string
	ModelName       *string
	ProviderName    *string
	ToolCallID      *string
	ToolName        *string
	Status          uint8
	RequestID       *string
	Metadata        *model.JSONMap
}

// CreateMessage 创建消息（自动生成 message_id 和 sequence_no）
func (s *chatMessageServiceImpl) CreateMessage(ctx context.Context, p *CreateMessageParams) (*model.ChatMessage, error) {
	// 获取当前会话最大 sequence_no
	maxSeq, err := model.GetMaxSequenceNo(ctx, s.db, p.ChatSessionID)
	if err != nil {
		return nil, err
	}

	status := p.Status
	if status == 0 {
		status = model.MessageStatusCompleted
	}

	msg := &model.ChatMessage{
		MessageID:       uuidgen.New(),
		ChatSessionID:   p.ChatSessionID,
		TurnID:          p.TurnID,
		ParentMessageID: p.ParentMessageID,
		SequenceNo:      maxSeq + 1,
		Role:            p.Role,
		MessageType:     p.MessageType,
		Content:         p.Content,
		ModelName:       p.ModelName,
		ProviderName:    p.ProviderName,
		ToolCallID:      p.ToolCallID,
		ToolName:        p.ToolName,
		Status:          status,
		RequestID:       p.RequestID,
		Metadata:        p.Metadata,
	}

	if err := model.CreateChatMessage(ctx, s.db, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// FindByID 根据主键 ID 查询
func (s *chatMessageServiceImpl) FindByID(ctx context.Context, id uint64) (*model.ChatMessage, error) {
	return model.FindChatMessageByID(ctx, s.db, id)
}

// FindByMessageID 根据对外 message_id 查询
func (s *chatMessageServiceImpl) FindByMessageID(ctx context.Context, messageID string) (*model.ChatMessage, error) {
	return model.FindChatMessageByMessageID(ctx, s.db, messageID)
}

// ListBySessionID 分页查询会话内消息
func (s *chatMessageServiceImpl) ListBySessionID(ctx context.Context, sessionID uint64, offset, limit int) ([]model.ChatMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	return model.FindChatMessagesBySessionID(ctx, s.db, sessionID, offset, limit)
}

// ListByTurnID 查询某轮次消息
func (s *chatMessageServiceImpl) ListByTurnID(ctx context.Context, sessionID uint64, turnID string) ([]model.ChatMessage, error) {
	return model.FindChatMessagesByTurnID(ctx, s.db, sessionID, turnID)
}

// UpdateStatus 更新状态和结束原因
func (s *chatMessageServiceImpl) UpdateStatus(ctx context.Context, id uint64, status uint8, finishReason *string) error {
	return model.UpdateChatMessageStatus(ctx, s.db, id, status, finishReason)
}

// UpdateContent 更新内容（流式输出完成后回填）
func (s *chatMessageServiceImpl) UpdateContent(ctx context.Context, id uint64, content string) error {
	return model.UpdateChatMessageContent(ctx, s.db, id, content)
}

// UpdateTokens 更新 Token 统计
func (s *chatMessageServiceImpl) UpdateTokens(ctx context.Context, id uint64, prompt, completion, total uint32) error {
	return model.UpdateChatMessageTokens(ctx, s.db, id, prompt, completion, total)
}

// SetError 记录错误信息并标记失败
func (s *chatMessageServiceImpl) SetError(ctx context.Context, id uint64, errorCode, errorMsg string) error {
	return model.UpdateChatMessageError(ctx, s.db, id, errorCode, errorMsg)
}

// SoftDelete 软删除消息
func (s *chatMessageServiceImpl) SoftDelete(ctx context.Context, id uint64) error {
	return model.SoftDeleteChatMessage(ctx, s.db, id)
}

// CreateToolResult 创建工具返回结果消息（快捷方法）
func (s *chatMessageServiceImpl) CreateToolResult(ctx context.Context,
	sessionID uint64, turnID, toolCallID, toolName string, content string, requestID *string,
) (*model.ChatMessage, error) {
	return s.CreateMessage(ctx, &CreateMessageParams{
		ChatSessionID: sessionID,
		TurnID:        &turnID,
		Role:          model.RoleTool,
		MessageType:   model.MessageTypeToolResult,
		Content:       &content,
		ToolCallID:    &toolCallID,
		ToolName:      &toolName,
		Status:        model.MessageStatusCompleted,
		RequestID:     requestID,
	})
}

// RecordMessageAndUpdate 创建消息并同步更新会话最后消息信息
func (s *chatMessageServiceImpl) RecordMessageAndUpdate(ctx context.Context, p *CreateMessageParams) (*model.ChatMessage, error) {
	msg, err := s.CreateMessage(ctx, p)
	if err != nil {
		return nil, err
	}
	// 更新会话最后消息时间
	_ = model.UpdateChatSessionLastMessage(ctx, s.db, p.ChatSessionID, time.Now())
	return msg, nil
}
