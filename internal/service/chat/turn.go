package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agentframework "easygo-agent/internal/agent"
	"easygo-agent/internal/credential"
	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/uuidgen"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TurnService struct {
	db      *gorm.DB
	cipher  *credential.Cipher
	runtime *agentframework.RuntimeFactory
}

func NewTurnService(
	db *gorm.DB,
	cipher *credential.Cipher,
	runtime *agentframework.RuntimeFactory,
) *TurnService {
	return &TurnService{db: db, cipher: cipher, runtime: runtime}
}

type PreparedTurn struct {
	Turn     *model.ChatTurn
	Session  *model.ChatSession
	Config   *model.UserModelConfig
	Runner   *adk.Runner
	Messages []*schema.Message
}

type DuplicateRequestError struct {
	Turn *model.ChatTurn
}

func (e *DuplicateRequestError) Error() string { return "duplicate request" }

func (s *TurnService) Prepare(
	ctx context.Context,
	userID uint64,
	sessionExternalID string,
	requestID string,
	input string,
) (*PreparedTurn, error) {
	input = strings.TrimSpace(input)
	requestID = strings.TrimSpace(requestID)
	if input == "" || requestID == "" {
		return nil, errorcode.New(errorcode.InvalidParameter, "input and request_id are required")
	}

	session, err := model.FindChatSessionOwnedByUser(ctx, s.db, sessionExternalID, userID)
	if err != nil {
		return nil, err
	}
	if existing, findErr := model.FindTurnByRequest(ctx, s.db, session.ID, requestID); findErr == nil {
		return nil, &DuplicateRequestError{Turn: existing}
	} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
		return nil, errorcode.Wrap(errorcode.Database, findErr)
	}
	if session.CurrentModelConfigID == nil || session.CurrentModelRevision == nil {
		return nil, errorcode.New(errorcode.InvalidParameter, "session model is not configured")
	}

	config, err := model.FindModelConfig(ctx, s.db, userID, *session.CurrentModelConfigID)
	if err != nil {
		return nil, errorcode.New(errorcode.InvalidParameter, "model configuration is unavailable")
	}
	if !config.Enabled || config.Revision != *session.CurrentModelRevision {
		return nil, errorcode.New(errorcode.Conflict, "session model revision is unavailable")
	}
	credentialRow, err := model.FindCredential(ctx, s.db, userID, config.CredentialID)
	if err != nil || credentialRow.Status != model.CredentialStatusEnabled ||
		credentialRow.ProviderName != config.ProviderName {
		return nil, errorcode.New(errorcode.InvalidParameter, "model credential is unavailable")
	}
	apiKey, err := s.cipher.Decrypt(credential.Ciphertext{
		Ciphertext: credentialRow.Ciphertext,
		Nonce:      credentialRow.Nonce,
		Algorithm:  credentialRow.Algorithm,
		KeyVersion: credentialRow.KeyVersion,
	}, credential.AssociatedData(userID, credentialRow.ProviderName))
	if err != nil {
		return nil, errorcode.New(errorcode.Internal, "model credential cannot be decrypted")
	}

	baseURL := ""
	if config.BaseURL != nil {
		baseURL = *config.BaseURL
	}
	settings := map[string]any(nil)
	if config.Settings != nil {
		settings = map[string]any(*config.Settings)
	}
	runner, err := s.runtime.Build(ctx, agentframework.ModelSpec{
		Provider:        config.ProviderName,
		APIKey:          apiKey,
		Model:           config.ModelName,
		BaseURL:         baseURL,
		MaxOutputTokens: config.MaxOutputTokens,
		Settings:        settings,
	}, config.MaxContextTokens)
	if err != nil {
		return nil, errorcode.Wrap(errorcode.InvalidParameter, fmt.Errorf("build Eino runtime: %w", err))
	}

	prepared := &PreparedTurn{Session: session, Config: config, Runner: runner}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedSession model.ChatSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", session.ID, userID).
			First(&lockedSession).Error; err != nil {
			return err
		}
		if lockedSession.CurrentModelConfigID == nil || lockedSession.CurrentModelRevision == nil ||
			*lockedSession.CurrentModelConfigID != config.ID ||
			*lockedSession.CurrentModelRevision != config.Revision {
			return errorcode.New(errorcode.Conflict, "session model changed; retry the request")
		}

		var duplicate model.ChatTurn
		if err := tx.Where("chat_session_id = ? AND request_id = ?", lockedSession.ID, requestID).
			First(&duplicate).Error; err == nil {
			return &DuplicateRequestError{Turn: &duplicate}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		now := time.Now()
		if err := expireStaleTurn(tx, lockedSession.ID, now); err != nil {
			return err
		}
		var active model.ChatTurn
		if err := tx.Where("chat_session_id = ? AND active_slot = 1", lockedSession.ID).
			First(&active).Error; err == nil {
			return errorcode.New(errorcode.Conflict, "session already has an active turn")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		history, err := model.FindContextMessagesBySessionID(ctx, tx, lockedSession.ID)
		if err != nil {
			return err
		}
		maxSequence, err := model.GetMaxSequenceNo(ctx, tx, lockedSession.ID)
		if err != nil {
			return err
		}

		activeSlot := uint8(1)
		expiresAt := now.Add(s.runtime.Config().TurnTimeout)
		turn := &model.ChatTurn{
			TurnID:        uuidgen.New(),
			UserID:        userID,
			ChatSessionID: lockedSession.ID,
			ModelConfigID: config.ID,
			ModelRevision: config.Revision,
			AgentRevision: s.runtime.Config().Revision,
			RequestID:     requestID,
			Status:        model.TurnRunning,
			ActiveSlot:    &activeSlot,
			StartedAt:     &now,
			ExpiresAt:     &expiresAt,
		}
		if err := tx.Create(turn).Error; err != nil {
			return err
		}

		userMessage := &model.ChatMessage{
			MessageID:     uuidgen.New(),
			ChatSessionID: lockedSession.ID,
			TurnID:        &turn.TurnID,
			SequenceNo:    maxSequence + 1,
			Role:          schema.User,
			Content:       input,
			MessageType:   model.MessageTypeText,
			Status:        model.MessageStatusCompleted,
			RequestID:     &requestID,
		}
		if err := tx.Create(userMessage).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ChatSession{}).Where("id = ?", lockedSession.ID).
			Updates(map[string]any{
				"last_message_at": now,
				"message_count":   gorm.Expr("message_count + 1"),
			}).Error; err != nil {
			return err
		}

		messages := make([]*schema.Message, 0, len(history)+1)
		for _, historyMessage := range history {
			messages = append(messages, historyMessage.ToEinoMessage())
		}
		messages = append(messages, userMessage.ToEinoMessage())
		prepared.Turn = turn
		prepared.Session = &lockedSession
		prepared.Messages = messages
		return nil
	})
	if err != nil {
		var duplicate *DuplicateRequestError
		if errors.As(err, &duplicate) {
			return nil, duplicate
		}
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			if existing, findErr := model.FindTurnByRequest(ctx, s.db, session.ID, requestID); findErr == nil {
				return nil, &DuplicateRequestError{Turn: existing}
			}
			return nil, errorcode.New(errorcode.Conflict, "session already has an active turn")
		}
		var appError *errorcode.Error
		if errors.As(err, &appError) {
			return nil, appError
		}
		return nil, errorcode.Wrap(errorcode.Database, err)
	}
	return prepared, nil
}

func expireStaleTurn(tx *gorm.DB, sessionID uint64, now time.Time) error {
	code := "execution_timeout"
	message := "Turn exceeded its execution deadline"
	return tx.Model(&model.ChatTurn{}).
		Where("chat_session_id = ? AND active_slot = 1 AND expires_at <= ?", sessionID, now).
		Updates(map[string]any{
			"status":        model.TurnFailed,
			"active_slot":   nil,
			"error_code":    code,
			"error_message": message,
			"completed_at":  now,
		}).Error
}

func (s *TurnService) Finalize(
	ctx context.Context,
	prepared *PreparedTurn,
	status uint8,
	outputs []*schema.Message,
	errorCode string,
	errorMessage string,
) error {
	if prepared == nil || prepared.Turn == nil {
		return fmt.Errorf("prepared turn is required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedSession model.ChatSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", prepared.Turn.ChatSessionID).
			First(&lockedSession).Error; err != nil {
			return err
		}
		var lockedTurn model.ChatTurn
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", prepared.Turn.ID).
			First(&lockedTurn).Error; err != nil {
			return err
		}
		if lockedTurn.Status != model.TurnRunning {
			return nil
		}

		maxSequence, err := model.GetMaxSequenceNo(ctx, tx, lockedSession.ID)
		if err != nil {
			return err
		}
		messageStatus := messageStatusForTurn(status)
		now := time.Now()
		persistedOutputs := 0
		for _, output := range outputs {
			if output == nil {
				continue
			}
			persistedOutputs++
			message := &model.ChatMessage{
				MessageID:     uuidgen.New(),
				ChatSessionID: lockedSession.ID,
				TurnID:        &lockedTurn.TurnID,
				SequenceNo:    maxSequence + uint32(persistedOutputs),
				MessageType:   messageTypeFor(output),
				ModelName:     &prepared.Config.ModelName,
				ProviderName:  &prepared.Config.ProviderName,
				Status:        messageStatus,
				RequestID:     &lockedTurn.RequestID,
			}
			message.ApplyEinoMessage(output)
			if status != model.TurnCompleted {
				message.ErrorCode = stringPointer(errorCode)
				message.ErrorMessage = stringPointer(truncate(errorMessage, 1000))
			}
			if err := tx.Create(message).Error; err != nil {
				return err
			}
		}

		updates := map[string]any{
			"status":       status,
			"active_slot":  nil,
			"completed_at": now,
		}
		if errorCode != "" {
			updates["error_code"] = errorCode
		} else {
			updates["error_code"] = nil
		}
		if errorMessage != "" {
			updates["error_message"] = truncate(errorMessage, 1000)
		} else {
			updates["error_message"] = nil
		}
		if err := tx.Model(&model.ChatTurn{}).Where("id = ?", lockedTurn.ID).Updates(updates).Error; err != nil {
			return err
		}
		if persistedOutputs > 0 {
			if err := tx.Model(&model.ChatSession{}).Where("id = ?", lockedSession.ID).
				Updates(map[string]any{
					"last_message_at": now,
					"message_count":   gorm.Expr("message_count + ?", persistedOutputs),
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *TurnService) Get(ctx context.Context, userID uint64, turnID string) (*model.ChatTurn, error) {
	return model.FindTurn(ctx, s.db, userID, turnID)
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
		return nil, errorcode.Wrap(errorcode.Database, err)
	}
	result := make([]SessionSummary, 0, len(sessions))
	for _, session := range sessions {
		result = append(result, SessionSummary{
			SessionID:            session.SessionID,
			Title:                session.Title,
			Status:               session.Status,
			LastMessageAt:        session.LastMessageAt,
			MessageCount:         session.MessageCount,
			CurrentModelConfigID: session.CurrentModelConfigID,
			CreatedAt:            session.CreatedAt,
		})
	}
	return result, nil
}

type MessageView struct {
	MessageID        string          `json:"message_id"`
	TurnID           *string         `json:"turn_id"`
	SequenceNo       uint32          `json:"sequence_no"`
	Role             schema.RoleType `json:"role"`
	Content          string          `json:"content"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ModelName        *string         `json:"model_name"`
	Status           uint8           `json:"status"`
	FinishReason     *string         `json:"finish_reason"`
	ErrorCode        *string         `json:"error_code"`
	ErrorMessage     *string         `json:"error_message"`
	CreatedAt        time.Time       `json:"created_at"`
}

func (s *TurnService) ListMessages(
	ctx context.Context,
	userID uint64,
	sessionExternalID string,
	limit int,
) ([]MessageView, error) {
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
	result := make([]MessageView, 0, len(messages))
	for _, message := range messages {
		var finishReason *string
		if message.ResponseMeta != nil && message.ResponseMeta.FinishReason != "" {
			finish := message.ResponseMeta.FinishReason
			finishReason = &finish
		}
		result = append(result, MessageView{
			MessageID:        message.MessageID,
			TurnID:           message.TurnID,
			SequenceNo:       message.SequenceNo,
			Role:             message.Role,
			Content:          message.Content,
			ReasoningContent: message.ReasoningContent,
			ModelName:        message.ModelName,
			Status:           message.Status,
			FinishReason:     finishReason,
			ErrorCode:        message.ErrorCode,
			ErrorMessage:     message.ErrorMessage,
			CreatedAt:        message.CreatedAt,
		})
	}
	return result, nil
}

func (s *TurnService) CreateSession(
	ctx context.Context,
	userID uint64,
	title string,
	modelConfigID uint64,
) (*model.ChatSession, error) {
	config, err := model.FindModelConfig(ctx, s.db, userID, modelConfigID)
	if err != nil {
		return nil, err
	}
	if !config.Enabled || !s.runtime.Supports(config.ProviderName) {
		return nil, errorcode.New(errorcode.InvalidParameter, "model config is unavailable")
	}
	session := &model.ChatSession{
		SessionID:            uuidgen.New(),
		UserID:               userID,
		Title:                title,
		Status:               model.ChatSessionStatusNormal,
		CurrentModelConfigID: &config.ID,
		CurrentModelRevision: &config.Revision,
	}
	return session, model.CreateChatSession(ctx, s.db, session)
}

func (s *TurnService) SwitchModel(
	ctx context.Context,
	userID uint64,
	sessionExternalID string,
	modelConfigID uint64,
) error {
	config, err := model.FindModelConfig(ctx, s.db, userID, modelConfigID)
	if err != nil {
		return err
	}
	if !config.Enabled || !s.runtime.Supports(config.ProviderName) {
		return errorcode.New(errorcode.InvalidParameter, "model config is unavailable")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session model.ChatSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("session_id = ? AND user_id = ?", sessionExternalID, userID).
			First(&session).Error; err != nil {
			return err
		}
		now := time.Now()
		if err := expireStaleTurn(tx, session.ID, now); err != nil {
			return err
		}
		var active model.ChatTurn
		if err := tx.Where("chat_session_id = ? AND active_slot = 1", session.ID).
			First(&active).Error; err == nil {
			return errorcode.New(errorcode.Conflict, "session already has an active turn")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Model(&model.ChatSession{}).Where("id = ?", session.ID).Updates(map[string]any{
			"current_model_config_id": config.ID,
			"current_model_revision":  config.Revision,
		}).Error
	})
}

func messageStatusForTurn(status uint8) uint8 {
	switch status {
	case model.TurnCompleted:
		return model.MessageStatusCompleted
	case model.TurnCancelled:
		return model.MessageStatusCancelled
	default:
		return model.MessageStatusFailed
	}
}

func messageTypeFor(message *schema.Message) uint8 {
	if len(message.UserInputMultiContent) > 0 || len(message.AssistantGenMultiContent) > 0 {
		return model.MessageTypeMultimodal
	}
	if len(message.ToolCalls) > 0 {
		return model.MessageTypeToolCall
	}
	if message.Role == schema.Tool {
		return model.MessageTypeToolResult
	}
	return model.MessageTypeText
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
