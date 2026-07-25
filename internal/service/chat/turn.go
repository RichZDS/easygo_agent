package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/uuidgen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TurnService struct {
	db            *gorm.DB
	events        TurnEventSink
	cancellations *CancellationCoordinator
}

func NewTurnService(db *gorm.DB, events TurnEventSink, cancellations *CancellationCoordinator) *TurnService {
	return &TurnService{db: db, events: events, cancellations: cancellations}
}
func (s *TurnService) Create(ctx context.Context, userID uint64, sessionExternalID, requestID, input string) (*model.ChatTurn, error) {
	if input == "" || requestID == "" {
		return nil, errorcode.New(errorcode.InvalidParameter, "input and request_id are required")
	}
	session, err := model.FindChatSessionOwnedByUser(ctx, s.db, sessionExternalID, userID)
	if err != nil {
		return nil, err
	}
	if session.CurrentModelConfigID == nil || session.CurrentModelRevision == nil {
		return nil, errorcode.New(errorcode.InvalidParameter, "session model is not configured")
	}
	if existing, err := model.FindOpenTurnForSession(ctx, s.db, session.ID); err == nil && existing.ID > 0 {
		return nil, errorcode.New(errorcode.Conflict, "session is busy")
	}
	var result *model.ChatTurn
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.ChatTurn
		if err := tx.Where("chat_session_id = ? AND request_id = ?", session.ID, requestID).First(&existing).Error; err == nil {
			result = &existing
			return nil
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		turn := &model.ChatTurn{TurnID: uuidgen.New(), UserID: userID, ChatSessionID: session.ID, ModelConfigID: *session.CurrentModelConfigID, ModelRevision: *session.CurrentModelRevision, RequestID: requestID, Input: input, Status: model.TurnPending}
		if err := tx.Create(turn).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.ChatOutbox{TurnID: turn.TurnID, UserID: userID, Status: model.OutboxPending}).Error; err != nil {
			return err
		}
		result = turn
		return nil
	})
	return result, err
}
func (s *TurnService) Get(ctx context.Context, userID uint64, turnID string) (*model.ChatTurn, error) {
	return model.FindTurn(ctx, s.db, userID, turnID)
}

func (s *TurnService) Cancel(ctx context.Context, userID uint64, turnID string) (*model.ChatTurn, error) {
	var result model.ChatTurn
	previousStatus := uint8(0)
	transitioned := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("turn_id = ? AND user_id = ?", turnID, userID).
			First(&result).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errorcode.New(errorcode.NotFound, "turn not found")
			}
			return err
		}
		previousStatus = result.Status
		if result.Status != model.TurnPending && result.Status != model.TurnRunning {
			return nil
		}
		now := time.Now()
		if err := tx.Model(&model.ChatTurn{}).
			Where("id = ? AND status = ?", result.ID, result.Status).
			Updates(map[string]any{"status": model.TurnCancelled, "completed_at": now}).Error; err != nil {
			return err
		}
		result.Status = model.TurnCancelled
		result.CompletedAt = &now
		transitioned = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !transitioned {
		return &result, nil
	}
	if previousStatus == model.TurnRunning {
		s.cancellations.Cancel(turnID)
		return &result, nil
	}

	// A pending turn has no executor that could publish its terminal events.
	config, _ := model.FindModelConfig(ctx, s.db, userID, result.ModelConfigID)
	modelName := ""
	if config != nil {
		modelName = config.ModelName
	}
	if err := s.events.Emit(ctx, turnID, "chunk", newCompletionChunk(turnID, modelName, "", "cancelled", nil)); err != nil {
		return nil, err
	}
	if err := s.events.Emit(ctx, turnID, "cancelled", map[string]any{"status": "cancelled"}); err != nil {
		return nil, err
	}
	return &result, nil
}

type SessionSummary struct {
	SessionID            string     `json:"session_id"`
	Title                string     `json:"title"`
	Status               uint8      `json:"status"`
	LastMessageAt        *time.Time `json:"last_message_at"`
	MessageCount         uint32     `json:"message_count"`
	CurrentModelConfigID *uint64    `json:"current_model_config_id"`
	CreatedAt            time.Time  `json:"created_at"`
}

func (s *TurnService) ListSessions(ctx context.Context, userID uint64) ([]SessionSummary, error) {
	var sessions []model.ChatSession
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("COALESCE(last_message_at, created_at) DESC").
		Find(&sessions).Error; err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, SessionSummary{
			SessionID:            session.SessionID,
			Title:                session.Title,
			Status:               session.Status,
			LastMessageAt:        session.LastMessageAt,
			MessageCount:         session.MessageCount,
			CurrentModelConfigID: session.CurrentModelConfigID,
			CreatedAt:            session.CreatedAt,
		})
	}
	return out, nil
}

type MessageView struct {
	MessageID    string    `json:"message_id"`
	TurnID       *string   `json:"turn_id"`
	SequenceNo   uint32    `json:"sequence_no"`
	Role         uint8     `json:"role"`
	Content      *string   `json:"content"`
	ModelName    *string   `json:"model_name"`
	Status       uint8     `json:"status"`
	FinishReason *string   `json:"finish_reason"`
	ErrorCode    *string   `json:"error_code"`
	ErrorMessage *string   `json:"error_message"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *TurnService) ListMessages(ctx context.Context, userID uint64, sessionExternalID string, limit int) ([]MessageView, error) {
	session, err := model.FindChatSessionOwnedByUser(ctx, s.db, sessionExternalID, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	messages, err := model.FindRecentChatMessagesBySessionID(ctx, s.db, session.ID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]MessageView, 0, len(messages))
	for _, message := range messages {
		out = append(out, MessageView{
			MessageID:    message.MessageID,
			TurnID:       message.TurnID,
			SequenceNo:   message.SequenceNo,
			Role:         message.Role,
			Content:      message.Content,
			ModelName:    message.ModelName,
			Status:       message.Status,
			FinishReason: message.FinishReason,
			ErrorCode:    message.ErrorCode,
			ErrorMessage: message.ErrorMessage,
			CreatedAt:    message.CreatedAt,
		})
	}
	return out, nil
}
func (s *TurnService) CreateSession(ctx context.Context, userID uint64, title string, modelConfigID uint64) (*model.ChatSession, error) {
	config, err := model.FindModelConfig(ctx, s.db, userID, modelConfigID)
	if err != nil {
		return nil, err
	}
	if !config.Enabled {
		return nil, errorcode.New(errorcode.InvalidParameter, "model config is disabled")
	}
	session := &model.ChatSession{SessionID: uuidgen.New(), UserID: userID, Title: title, Status: model.ChatSessionStatusNormal, CurrentModelConfigID: &config.ID, CurrentModelRevision: &config.Revision}
	return session, model.CreateChatSession(ctx, s.db, session)
}
func (s *TurnService) SwitchModel(ctx context.Context, userID uint64, sessionExternalID string, modelConfigID uint64) error {
	session, err := model.FindChatSessionOwnedByUser(ctx, s.db, sessionExternalID, userID)
	if err != nil {
		return err
	}
	if active, err := model.FindOpenTurnForSession(ctx, s.db, session.ID); err == nil && active.ID > 0 {
		return errorcode.New(errorcode.Conflict, "session is busy")
	}
	config, err := model.FindModelConfig(ctx, s.db, userID, modelConfigID)
	if err != nil {
		return err
	}
	if !config.Enabled {
		return errorcode.New(errorcode.InvalidParameter, "model config is disabled")
	}
	if err := s.summarizeForBudget(ctx, session, config); err != nil {
		return err
	}
	return model.UpdateChatSessionModel(ctx, s.db, session.ID, config.ID, config.Revision)
}

// summarizeForBudget creates a compact, auditable digest before a smaller
// context model replaces the session model. Raw messages remain in MySQL. The
// digest is deliberately bounded to half the new history allowance.
func (s *TurnService) summarizeForBudget(ctx context.Context, session *model.ChatSession, config *model.UserModelConfig) error {
	budget := int(config.MaxContextTokens) / 5
	messages, err := model.FindRecentChatMessagesBySessionID(ctx, s.db, session.ID, 500)
	if err != nil {
		return err
	}
	total := 0
	for _, m := range messages {
		total += m.EstimateTokens()
	}
	if total <= budget {
		return nil
	}
	need := total - budget
	selected := make([]model.ChatMessage, 0)
	selectedTokens := 0
	for _, m := range messages {
		if selectedTokens >= need {
			break
		}
		selected = append(selected, m)
		selectedTokens += m.EstimateTokens()
	}
	if len(selected) == 0 {
		return nil
	}
	maxChars := budget * 2
	var b strings.Builder
	b.WriteString("Historical conversation summary:\n")
	for _, m := range selected {
		if m.Content == nil {
			continue
		}
		role := "assistant"
		if m.Role == model.RoleUser {
			role = "user"
		}
		line := fmt.Sprintf("%s: %s\n", role, *m.Content)
		if b.Len()+len(line) > maxChars {
			break
		}
		b.WriteString(line)
	}
	content := b.String()
	return s.db.WithContext(ctx).Create(&model.ChatSummary{ChatSessionID: session.ID, FromSequenceNo: selected[0].SequenceNo, ToSequenceNo: selected[len(selected)-1].SequenceNo, Content: content, TokenCount: uint32(model.EstimateTokens(content)), ModelConfigID: config.ID, ModelRevision: config.Revision}).Error
}
func (s *TurnService) PublishPending(ctx context.Context, publish func(context.Context, uint64, string) (string, error)) error {
	var rows []model.ChatOutbox
	if err := s.db.WithContext(ctx).Where("status = ?", model.OutboxPending).Order("id").Limit(100).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		streamID, err := publish(ctx, row.UserID, row.TurnID)
		if err != nil {
			_ = s.db.WithContext(ctx).Model(&model.ChatOutbox{}).Where("id = ?", row.ID).UpdateColumn("attempts", gorm.Expr("attempts + 1")).Error
			continue
		}
		now := time.Now()
		if err := s.db.WithContext(ctx).Model(&model.ChatOutbox{}).Where("id = ? AND status = ?", row.ID, model.OutboxPending).Updates(map[string]any{"status": model.OutboxPublished, "stream_id": streamID, "published_at": now, "attempts": gorm.Expr("attempts + 1")}).Error; err != nil {
			return fmt.Errorf("mark outbox published: %w", err)
		}
	}
	return nil
}

// PublishedUserIDs is bounded so an idle process can poll active user streams
// without a global stream that would violate per-user FIFO ordering.
func (s *TurnService) PublishedUserIDs(ctx context.Context) ([]uint64, error) {
	var rows []uint64
	err := s.db.WithContext(ctx).Model(&model.ChatOutbox{}).Distinct("user_id").Where("status = ?", model.OutboxPublished).Limit(500).Pluck("user_id", &rows).Error
	return rows, err
}

func (s *TurnService) MarkRunning(ctx context.Context, turnID string) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&model.ChatTurn{}).Where("turn_id = ? AND status = ?", turnID, model.TurnPending).Updates(map[string]any{"status": model.TurnRunning, "started_at": now}).Error
}
