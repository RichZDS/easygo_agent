package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"easygo-agent/internal/platform/errorcode"

	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
)

const ChatMessageSchemaVersion uint32 = 1

// ChatMessage maps to table `chat_message`.
type ChatMessage struct {
	ID            uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	SessionID     uint64    `gorm:"column:session_id;not null;uniqueIndex:uk_session_seq,priority:1" json:"session_id"`
	AgentRunID    *uint64   `gorm:"column:agent_run_id;index" json:"agent_run_id"`
	Seq           uint64    `gorm:"column:seq;not null;uniqueIndex:uk_session_seq,priority:2" json:"seq"`
	Role          string    `gorm:"column:role;type:varchar(16);not null" json:"role"`
	Text          string    `gorm:"column:text;type:mediumtext;not null" json:"text"`
	Metadata      JSONMap   `gorm:"column:metadata;type:json" json:"metadata"`
	Raw           string    `gorm:"column:raw;type:json;not null" json:"raw"`
	SchemaVersion uint32    `gorm:"column:schema_version;not null;default:1" json:"schema_version"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
}

func (ChatMessage) TableName() string { return "chat_message" }

func CreateChatMessage(ctx context.Context, db *gorm.DB, row *ChatMessage) error {
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("create chat message: %w", err))
	}
	return nil
}

func CreateChatMessages(ctx context.Context, db *gorm.DB, rows []*ChatMessage) error {
	if len(rows) == 0 {
		return nil
	}
	if err := db.WithContext(ctx).Create(&rows).Error; err != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("batch create chat messages: %w", err))
	}
	return nil
}

func ListRecentMessages(ctx context.Context, db *gorm.DB, sessionID uint64, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 40
	}
	var rows []ChatMessage
	if err := db.WithContext(ctx).Where("session_id = ?", sessionID).
		Order("seq DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("list recent messages: %w", err))
	}
	// Reverse to ascending seq.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

func ListMessagesBySession(ctx context.Context, db *gorm.DB, sessionID uint64, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []ChatMessage
	if err := db.WithContext(ctx).Where("session_id = ?", sessionID).
		Order("seq ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("list messages: %w", err))
	}
	return rows, nil
}

func NewChatMessageFromAgentic(
	sessionID uint64,
	agentRunID *uint64,
	seq uint64,
	msg *schema.AgenticMessage,
) (*ChatMessage, error) {
	if msg == nil {
		return nil, fmt.Errorf("agentic message is nil")
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal agentic message: %w", err)
	}
	var metadata JSONMap
	if len(msg.Extra) > 0 {
		metadata = JSONMap(msg.Extra)
	}
	return &ChatMessage{
		SessionID:     sessionID,
		AgentRunID:    agentRunID,
		Seq:           seq,
		Role:          string(msg.Role),
		Text:          ExtractAgenticText(msg),
		Metadata:      metadata,
		Raw:           string(raw),
		SchemaVersion: ChatMessageSchemaVersion,
	}, nil
}

func (m *ChatMessage) ToAgenticMessage() (*schema.AgenticMessage, error) {
	var msg schema.AgenticMessage
	if err := json.Unmarshal([]byte(m.Raw), &msg); err != nil {
		return nil, fmt.Errorf("unmarshal agentic message: %w", err)
	}
	return &msg, nil
}

func ExtractAgenticText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	parts := make([]string, 0, len(msg.ContentBlocks))
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		if block.UserInputText != nil && block.UserInputText.Text != "" {
			parts = append(parts, block.UserInputText.Text)
		}
		if block.AssistantGenText != nil && block.AssistantGenText.Text != "" {
			parts = append(parts, block.AssistantGenText.Text)
		}
	}
	return strings.Join(parts, "\n")
}
