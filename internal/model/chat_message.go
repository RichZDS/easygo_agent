package model

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"easygo-agent/internal/errorcode"

	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
)

const (
	RoleSystem    = schema.System
	RoleUser      = schema.User
	RoleAssistant = schema.Assistant
	RoleTool      = schema.Tool
)

const (
	MessageTypeText       uint8 = 1
	MessageTypeImage      uint8 = 2
	MessageTypeFile       uint8 = 3
	MessageTypeToolCall   uint8 = 4
	MessageTypeToolResult uint8 = 5
	MessageTypeMultimodal uint8 = 6
)

const (
	MessageStatusGenerating uint8 = 1
	MessageStatusCompleted  uint8 = 2
	MessageStatusFailed     uint8 = 3
	MessageStatusCancelled  uint8 = 4
)

// ChatMessage stores an Eino schema.Message payload inside an EasyGo-owned
// persistence envelope. Eino fields intentionally keep their upstream names
// and types so conversion at the runtime boundary stays mechanical.
type ChatMessage struct {
	ID              uint64  `gorm:"column:id;type:bigint unsigned;primaryKey;autoIncrement" json:"id"`
	MessageID       string  `gorm:"column:message_id;type:varchar(64);not null;uniqueIndex:uk_message_id" json:"message_id"`
	ChatSessionID   uint64  `gorm:"column:chat_session_id;type:bigint unsigned;not null;index:idx_session_created,priority:1;index:idx_session_turn,priority:1;uniqueIndex:uk_session_sequence,priority:1" json:"chat_session_id"`
	TurnID          *string `gorm:"column:turn_id;type:varchar(64);index:idx_session_turn,priority:2" json:"turn_id"`
	ParentMessageID *string `gorm:"column:parent_message_id;type:varchar(64);index:idx_parent_message" json:"parent_message_id"`
	SequenceNo      uint32  `gorm:"column:sequence_no;type:int unsigned;not null;uniqueIndex:uk_session_sequence,priority:2" json:"sequence_no"`

	Role                     schema.RoleType            `gorm:"column:role;type:varchar(16);not null" json:"role"`
	Content                  string                     `gorm:"column:content;type:mediumtext;not null" json:"content"`
	UserInputMultiContent    []schema.MessageInputPart  `gorm:"column:user_input_multi_content;type:json;serializer:json" json:"user_input_multi_content,omitempty"`
	AssistantGenMultiContent []schema.MessageOutputPart `gorm:"column:assistant_output_multi_content;type:json;serializer:json" json:"assistant_output_multi_content,omitempty"`
	Name                     string                     `gorm:"column:name;type:varchar(128)" json:"name,omitempty"`
	ToolCalls                []schema.ToolCall          `gorm:"column:tool_calls;type:json;serializer:json" json:"tool_calls,omitempty"`
	ToolCallID               string                     `gorm:"column:tool_call_id;type:varchar(128)" json:"tool_call_id,omitempty"`
	ToolName                 string                     `gorm:"column:tool_name;type:varchar(128)" json:"tool_name,omitempty"`
	ResponseMeta             *schema.ResponseMeta       `gorm:"column:response_meta;type:json;serializer:json" json:"response_meta,omitempty"`
	ReasoningContent         string                     `gorm:"column:reasoning_content;type:mediumtext" json:"reasoning_content,omitempty"`
	Extra                    map[string]any             `gorm:"column:extra;type:json;serializer:json" json:"extra,omitempty"`

	// EasyGo persistence and audit fields.
	MessageType  uint8          `gorm:"column:message_type;type:tinyint unsigned;not null;default:1" json:"message_type"`
	ModelName    *string        `gorm:"column:model_name;type:varchar(128)" json:"model_name,omitempty"`
	ProviderName *string        `gorm:"column:provider_name;type:varchar(64)" json:"provider_name,omitempty"`
	Status       uint8          `gorm:"column:status;type:tinyint unsigned;not null;default:2" json:"status"`
	RequestID    *string        `gorm:"column:request_id;type:varchar(64);index:idx_request_id" json:"request_id,omitempty"`
	ErrorCode    *string        `gorm:"column:error_code;type:varchar(64)" json:"error_code,omitempty"`
	ErrorMessage *string        `gorm:"column:error_message;type:varchar(1000)" json:"error_message,omitempty"`
	CreatedAt    time.Time      `gorm:"column:created_at;type:datetime(3);not null;autoCreateTime:milli;index:idx_session_created,priority:2" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"column:updated_at;type:datetime(3);not null;autoUpdateTime:milli" json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;type:datetime(3);index:idx_deleted_at" json:"-"`
}

func (ChatMessage) TableName() string { return "chat_message" }

func (m ChatMessage) ToEinoMessage() *schema.Message {
	return &schema.Message{
		Role:                     m.Role,
		Content:                  m.Content,
		UserInputMultiContent:    m.UserInputMultiContent,
		AssistantGenMultiContent: m.AssistantGenMultiContent,
		Name:                     m.Name,
		ToolCalls:                m.ToolCalls,
		ToolCallID:               m.ToolCallID,
		ToolName:                 m.ToolName,
		ResponseMeta:             m.ResponseMeta,
		ReasoningContent:         m.ReasoningContent,
		Extra:                    m.Extra,
	}
}

func (m *ChatMessage) ApplyEinoMessage(message *schema.Message) {
	if message == nil {
		return
	}
	m.Role = message.Role
	m.Content = message.Content
	m.UserInputMultiContent = message.UserInputMultiContent
	m.AssistantGenMultiContent = message.AssistantGenMultiContent
	m.Name = message.Name
	m.ToolCalls = message.ToolCalls
	m.ToolCallID = message.ToolCallID
	m.ToolName = message.ToolName
	m.ResponseMeta = message.ResponseMeta
	m.ReasoningContent = message.ReasoningContent
	m.Extra = message.Extra
}

func FindRecentChatMessagesBySessionID(ctx context.Context, db *gorm.DB, sessionID uint64, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	var descending []ChatMessage
	if err := db.WithContext(ctx).
		Where("chat_session_id = ?", sessionID).
		Order("sequence_no DESC").
		Limit(limit).
		Find(&descending).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find recent messages by session: %w", err))
	}
	for left, right := 0, len(descending)-1; left < right; left, right = left+1, right-1 {
		descending[left], descending[right] = descending[right], descending[left]
	}
	return descending, nil
}

// FindContextMessagesBySessionID returns only messages that are eligible as
// future model context. Partial failed or cancelled assistant output is durable
// and visible, but deliberately excluded here.
func FindContextMessagesBySessionID(ctx context.Context, db *gorm.DB, sessionID uint64) ([]ChatMessage, error) {
	var messages []ChatMessage
	if err := db.WithContext(ctx).
		Where("chat_session_id = ? AND status = ?", sessionID, MessageStatusCompleted).
		Order("sequence_no ASC").
		Find(&messages).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find context messages by session: %w", err))
	}
	return messages, nil
}

func GetMaxSequenceNo(ctx context.Context, db *gorm.DB, sessionID uint64) (uint32, error) {
	var maxSequence uint32
	if err := db.WithContext(ctx).
		Model(&ChatMessage{}).
		Where("chat_session_id = ?", sessionID).
		Select("COALESCE(MAX(sequence_no), 0)").
		Scan(&maxSequence).Error; err != nil {
		return 0, errorcode.Wrap(errorcode.Database, fmt.Errorf("get max sequence_no: %w", err))
	}
	return maxSequence, nil
}

// JSONMap is retained for model configuration settings.
type JSONMap map[string]any

func (m *JSONMap) Scan(value any) error {
	if value == nil {
		*m = nil
		return nil
	}
	var bytes []byte
	switch typed := value.(type) {
	case []byte:
		bytes = typed
	case string:
		bytes = []byte(typed)
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
