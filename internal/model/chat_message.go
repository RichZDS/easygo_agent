package model

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"easygo-agent/internal/errorcode"

	"gorm.io/gorm"
)

// ========== 枚举常量 ==========

const (
	RoleSystem    uint8 = 1 // 系统消息
	RoleUser      uint8 = 2 // 用户消息
	RoleAssistant uint8 = 3 // 助手消息
	RoleTool      uint8 = 4 // 工具消息
)

const (
	MessageTypeText       uint8 = 1 // 文本消息
	MessageTypeImage      uint8 = 2 // 图片消息
	MessageTypeFile       uint8 = 3 // 文件消息
	MessageTypeToolCall   uint8 = 4 // 工具调用消息
	MessageTypeToolResult uint8 = 5 // 工具结果消息
	MessageTypeMultimodal uint8 = 6 // 多模态消息
)

const (
	MessageStatusGenerating uint8 = 1 // 生成中
	MessageStatusCompleted  uint8 = 2 // 完成
	MessageStatusFailed     uint8 = 3 // 失败
	MessageStatusCancelled  uint8 = 4 // 取消
)

// ========== 结构体 ==========

// ChatMessage maps to the MySQL `chat_message` table.
type ChatMessage struct {
	ID               uint64         `gorm:"column:id;type:bigint unsigned;primaryKey;autoIncrement" json:"id"`                                                                                                                             // 主键 ID
	MessageID        string         `gorm:"column:message_id;type:varchar(64);not null;uniqueIndex:uk_message_id" json:"message_id"`                                                                                                       // 消息对外 ID
	ChatSessionID    uint64         `gorm:"column:chat_session_id;type:bigint unsigned;not null;index:idx_session_created,priority:1;index:idx_session_turn,priority:1;uniqueIndex:uk_session_sequence,priority:1" json:"chat_session_id"` // 会话 ID
	TurnID           *string        `gorm:"column:turn_id;type:varchar(64);index:idx_session_turn,priority:2" json:"turn_id"`                                                                                                              // 轮次 ID
	ParentMessageID  *string        `gorm:"column:parent_message_id;type:varchar(64);index:idx_parent_message" json:"parent_message_id"`                                                                                                   // 父消息 ID
	SequenceNo       uint32         `gorm:"column:sequence_no;type:int unsigned;not null;uniqueIndex:uk_session_sequence,priority:2" json:"sequence_no"`                                                                                   // 消息序号
	Role             uint8          `gorm:"column:role;type:tinyint unsigned;not null" json:"role"`                                                                                                                                        // 消息角色
	MessageType      uint8          `gorm:"column:message_type;type:tinyint unsigned;not null;default:1" json:"message_type"`                                                                                                              // 消息类型
	Content          *string        `gorm:"column:content;type:mediumtext" json:"content"`                                                                                                                                                 // 消息内容
	ModelName        *string        `gorm:"column:model_name;type:varchar(128)" json:"model_name"`                                                                                                                                         // 模型名称
	ProviderName     *string        `gorm:"column:provider_name;type:varchar(64)" json:"provider_name"`                                                                                                                                    // 提供者名称
	ToolCallID       *string        `gorm:"column:tool_call_id;type:varchar(128)" json:"tool_call_id"`                                                                                                                                     // 工具调用 ID
	ToolName         *string        `gorm:"column:tool_name;type:varchar(128)" json:"tool_name"`                                                                                                                                           // 工具名称
	Status           uint8          `gorm:"column:status;type:tinyint unsigned;not null;default:2" json:"status"`                                                                                                                          // 消息状态
	FinishReason     *string        `gorm:"column:finish_reason;type:varchar(32)" json:"finish_reason"`                                                                                                                                    // 结束原因
	PromptTokens     uint32         `gorm:"column:prompt_tokens;type:int unsigned;not null;default:0" json:"prompt_tokens"`                                                                                                                // 提示 Token 数量
	CompletionTokens uint32         `gorm:"column:completion_tokens;type:int unsigned;not null;default:0" json:"completion_tokens"`                                                                                                        // 完成 Token 数量
	TotalTokens      uint32         `gorm:"column:total_tokens;type:int unsigned;not null;default:0" json:"total_tokens"`                                                                                                                  // 总 Token 数量
	RequestID        *string        `gorm:"column:request_id;type:varchar(64);index:idx_request_id" json:"request_id"`                                                                                                                     // 请求 ID
	ErrorCode        *string        `gorm:"column:error_code;type:varchar(64)" json:"error_code"`                                                                                                                                          // 错误代码
	ErrorMessage     *string        `gorm:"column:error_message;type:varchar(1000)" json:"error_message"`                                                                                                                                  // 错误消息
	Metadata         *JSONMap       `gorm:"column:metadata;type:json" json:"metadata"`                                                                                                                                                     // 元数据
	CreatedAt        time.Time      `gorm:"column:created_at;type:datetime(3);not null;autoCreateTime:milli;index:idx_session_created,priority:2" json:"created_at"`                                                                       // 创建时间
	UpdatedAt        time.Time      `gorm:"column:updated_at;type:datetime(3);not null;autoUpdateTime:milli" json:"updated_at"`                                                                                                            // 更新时间
	DeletedAt        gorm.DeletedAt `gorm:"column:deleted_at;type:datetime(3);index:idx_deleted_at" json:"-"`                                                                                                                              // 删除时间
}

func (ChatMessage) TableName() string { return "chat_message" }

// ========== GORM 包级查询函数 ==========

// CreateChatMessage 创建消息。调用方需自行设置 SequenceNo。
func CreateChatMessage(ctx context.Context, db *gorm.DB, msg *ChatMessage) error {
	if err := db.WithContext(ctx).Create(msg).Error; err != nil {
		return mapChatMessageWriteError("create chat message", err)
	}
	return nil
}

// FindChatMessageByID 根据主键 ID 查询
func FindChatMessageByID(ctx context.Context, db *gorm.DB, id uint64) (*ChatMessage, error) {
	return findOneChatMessage(db.WithContext(ctx).Where("id = ?", id))
}

// FindChatMessageByMessageID 根据对外 message_id 查询
func FindChatMessageByMessageID(ctx context.Context, db *gorm.DB, messageID string) (*ChatMessage, error) {
	return findOneChatMessage(db.WithContext(ctx).Where("message_id = ?", messageID))
}

// FindChatMessagesBySessionID 分页查询会话内消息，按 sequence_no 正序
func FindChatMessagesBySessionID(ctx context.Context, db *gorm.DB, sessionID uint64, offset, limit int) ([]ChatMessage, error) {
	var msgs []ChatMessage
	if err := db.WithContext(ctx).
		Where("chat_session_id = ?", sessionID).
		Order("sequence_no ASC").
		Offset(offset).
		Limit(limit).
		Find(&msgs).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find messages by session: %w", err))
	}
	return msgs, nil
}

// FindChatMessagesByTurnID 查询某轮次的所有消息
func FindChatMessagesByTurnID(ctx context.Context, db *gorm.DB, sessionID uint64, turnID string) ([]ChatMessage, error) {
	var msgs []ChatMessage
	if err := db.WithContext(ctx).
		Where("chat_session_id = ? AND turn_id = ?", sessionID, turnID).
		Order("sequence_no ASC").
		Find(&msgs).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find messages by turn: %w", err))
	}
	return msgs, nil
}

// GetMaxSequenceNo 获取会话当前最大 sequence_no
func GetMaxSequenceNo(ctx context.Context, db *gorm.DB, sessionID uint64) (uint32, error) {
	var maxSeq uint32
	if err := db.WithContext(ctx).
		Model(&ChatMessage{}).
		Where("chat_session_id = ?", sessionID).
		Select("COALESCE(MAX(sequence_no), 0)").
		Scan(&maxSeq).Error; err != nil {
		return 0, errorcode.Wrap(errorcode.Database, fmt.Errorf("get max sequence_no: %w", err))
	}
	return maxSeq, nil
}

// UpdateChatMessageStatus 更新消息状态和结束原因
func UpdateChatMessageStatus(ctx context.Context, db *gorm.DB, id uint64, status uint8, finishReason *string) error {
	updates := map[string]any{"status": status}
	if finishReason != nil {
		updates["finish_reason"] = *finishReason
	}
	result := db.WithContext(ctx).Model(&ChatMessage{}).Where("id = ?", id).Updates(updates)
	return checkChatMessageUpdate("update message status", result)
}

// UpdateChatMessageContent 更新消息内容（流式输出完成后回填）
func UpdateChatMessageContent(ctx context.Context, db *gorm.DB, id uint64, content string) error {
	result := db.WithContext(ctx).Model(&ChatMessage{}).Where("id = ?", id).Update("content", content)
	return checkChatMessageUpdate("update message content", result)
}

// UpdateChatMessageTokens 更新 Token 统计
func UpdateChatMessageTokens(ctx context.Context, db *gorm.DB, id uint64, prompt, completion, total uint32) error {
	result := db.WithContext(ctx).Model(&ChatMessage{}).Where("id = ?", id).Updates(map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
	})
	return checkChatMessageUpdate("update message tokens", result)
}

// UpdateChatMessageError 记录错误信息
func UpdateChatMessageError(ctx context.Context, db *gorm.DB, id uint64, errorCode, errorMsg string) error {
	result := db.WithContext(ctx).Model(&ChatMessage{}).Where("id = ?", id).Updates(map[string]any{
		"status":        MessageStatusFailed,
		"error_code":    errorCode,
		"error_message": errorMsg,
	})
	return checkChatMessageUpdate("update message error", result)
}

// SoftDeleteChatMessage 软删除消息
func SoftDeleteChatMessage(ctx context.Context, db *gorm.DB, id uint64) error {
	result := db.WithContext(ctx).Delete(&ChatMessage{}, id)
	return checkChatMessageUpdate("delete chat message", result)
}

// ========== 内部辅助函数 ==========

func findOneChatMessage(query *gorm.DB) (*ChatMessage, error) {
	var msg ChatMessage
	if err := query.First(&msg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorcode.New(errorcode.NotFound, "消息不存在")
		}
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find chat message: %w", err))
	}
	return &msg, nil
}

func checkChatMessageUpdate(operation string, result *gorm.DB) error {
	if result.Error != nil {
		return mapChatMessageWriteError(operation, result.Error)
	}
	if result.RowsAffected == 0 {
		return errorcode.New(errorcode.NotFound, "消息不存在")
	}
	return nil
}

func mapChatMessageWriteError(operation string, err error) error {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return errorcode.New(errorcode.Conflict, "消息已存在")
	}
	return errorcode.Wrap(errorcode.Database, fmt.Errorf("%s: %w", operation, err))
}

// ========== JSONMap：MySQL JSON 列扫描/值转换 ==========

// JSONMap 用于 GORM 读写 MySQL JSON 列。
type JSONMap map[string]any

func (m *JSONMap) Scan(value any) error {
	if value == nil {
		*m = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("JSONMap.Scan: unsupported type %T", value)
	}
	return json.Unmarshal(bytes, m)
}

func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

// ========== Token 估算 ==========

// EstimateTokens 估算本消息的 token 消耗（基于 content 字段）。
// 仅用于 Redis 容量截断判断，与 LLM 返回的实际 total_tokens 不同。
func (m *ChatMessage) EstimateTokens() int {
	if m.Content == nil {
		return 0
	}
	return EstimateTokens(*m.Content)
}

// EstimateTokens 估算文本的 token 消耗。
// 英文/ASCII 字符（r <= 127）≈ 0.3 token，中文/多字节字符 ≈ 0.6 token。
// 使用 math.Ceil 向上取整避免零值累积导致计数偏小。
func EstimateTokens(text string) int {
	var tokens float64
	for _, r := range text {
		if r <= 127 {
			tokens += 0.3
		} else {
			tokens += 0.6
		}
	}
	return int(math.Ceil(tokens))
}

