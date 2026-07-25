package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"easygo-agent/internal/credential"
	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/uuidgen"
	chatcache "easygo-agent/internal/repository/chatcache"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const cancellationPollInterval = 250 * time.Millisecond

type ExecutionService struct {
	db            *gorm.DB
	cipher        *credential.Cipher
	cache         *chatcache.ChatCacheRepo
	streamer      ProviderStreamer
	events        TurnEventSink
	cancellations *CancellationCoordinator
}

func NewExecutionService(
	db *gorm.DB,
	cipher *credential.Cipher,
	cache *chatcache.ChatCacheRepo,
	streamer ProviderStreamer,
	events TurnEventSink,
	cancellations *CancellationCoordinator,
) *ExecutionService {
	return &ExecutionService{
		db:            db,
		cipher:        cipher,
		cache:         cache,
		streamer:      streamer,
		events:        events,
		cancellations: cancellations,
	}
}

// ProcessTurn records provider failures as terminal and returns nil so the
// queue entry can be acknowledged. Persistence and event infrastructure
// failures are returned so the queue can reclaim the entry.
func (s *ExecutionService) ProcessTurn(ctx context.Context, turnID string) error {
	turn, err := model.FindTurnInternal(ctx, s.db, turnID)
	if err != nil {
		return err
	}
	if isTerminalTurn(turn.Status) {
		return nil
	}
	if err := s.events.Emit(ctx, turnID, "status", map[string]any{"status": "running"}); err != nil {
		return err
	}

	config, err := model.FindModelConfig(ctx, s.db, turn.UserID, turn.ModelConfigID)
	if err != nil {
		return s.fail(ctx, turn, "", "MODEL_CONFIG", "model configuration is unavailable")
	}
	session, err := model.FindChatSessionByID(ctx, s.db, turn.ChatSessionID)
	if err != nil {
		return s.fail(ctx, turn, config.ModelName, "SESSION", "chat session is unavailable")
	}
	credentialRow, err := model.FindCredential(ctx, s.db, turn.UserID, config.CredentialID)
	if err != nil {
		return s.fail(ctx, turn, config.ModelName, "CREDENTIAL", "model credential is unavailable")
	}
	apiKey, err := s.cipher.Decrypt(credential.Ciphertext{
		Ciphertext: credentialRow.Ciphertext,
		Nonce:      credentialRow.Nonce,
		Algorithm:  credentialRow.Algorithm,
		KeyVersion: credentialRow.KeyVersion,
	}, []byte(fmt.Sprintf("user:%d:provider:%s", turn.UserID, credentialRow.ProviderName)))
	if err != nil {
		return s.fail(ctx, turn, config.ModelName, "CREDENTIAL", "model credential cannot be decrypted")
	}

	messages, err := s.contextMessages(ctx, turn, config, session.SessionID)
	if err != nil {
		return err
	}
	messages = append(messages, ProviderMessage{Role: "user", Content: turn.Input})

	providerCtx, cancelProvider := context.WithCancel(ctx)
	unregister := s.cancellations.Register(turnID, cancelProvider)
	defer unregister()
	defer cancelProvider()

	// Close the provider response promptly when a cancellation was handled by a
	// different application replica.
	watcherDone := make(chan struct{})
	go s.watchCancellation(providerCtx, turnID, cancelProvider, watcherDone)
	defer close(watcherDone)

	// Cover the small race between the initial turn read and registration.
	if cancelled, checkErr := s.turnIsCancelled(ctx, turnID); checkErr != nil {
		return checkErr
	} else if cancelled {
		cancelProvider()
	}

	var answer strings.Builder
	result, streamErr := s.streamer.Stream(providerCtx, ProviderStreamRequest{
		BaseURL:   resolveProviderBaseURL(config),
		APIKey:    apiKey,
		Model:     config.ModelName,
		Messages:  messages,
		MaxTokens: config.MaxOutputTokens,
	}, func(delta ProviderDelta) error {
		if delta.Content == "" {
			return nil
		}
		answer.WriteString(delta.Content)
		return s.events.Emit(providerCtx, turnID, "chunk", newCompletionChunk(turnID, config.ModelName, delta.Content, "", nil))
	})

	finalCtx, finalCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finalCancel()
	cancelled, statusErr := s.turnIsCancelled(finalCtx, turnID)
	if statusErr != nil {
		return statusErr
	}
	if cancelled {
		finalStatus, err := s.persistResult(finalCtx, turn, config, session.SessionID, answer.String(), result.Usage, "cancelled", model.MessageStatusCancelled, model.TurnCancelled, "", "")
		if err != nil {
			return err
		}
		if finalStatus == model.TurnCancelled {
			return s.emitCancelled(finalCtx, turnID, config.ModelName)
		}
		return nil
	}
	if streamErr != nil {
		if errors.Is(streamErr, context.Canceled) && ctx.Err() != nil {
			return ctx.Err()
		}
		if answer.Len() > 0 {
			if _, err := s.persistResult(finalCtx, turn, config, session.SessionID, answer.String(), result.Usage, "error", model.MessageStatusFailed, model.TurnFailed, "PROVIDER", "model request failed"); err != nil {
				return err
			}
			return s.events.Emit(finalCtx, turnID, "failed", map[string]any{"status": "failed", "error_code": "PROVIDER", "message": "model request failed"})
		}
		return s.fail(finalCtx, turn, config.ModelName, "PROVIDER", "model request failed")
	}

	finish := result.FinishReason
	if finish == "" {
		finish = "stop"
	}
	finalStatus, err := s.persistResult(finalCtx, turn, config, session.SessionID, answer.String(), result.Usage, finish, model.MessageStatusCompleted, model.TurnCompleted, "", "")
	if err != nil {
		return err
	}
	if finalStatus == model.TurnCancelled {
		return s.emitCancelled(finalCtx, turnID, config.ModelName)
	}
	if finalStatus != model.TurnCompleted {
		return nil
	}
	var terminalUsage *ProviderUsage
	if result.HasUsage {
		terminalUsage = &result.Usage
	}
	if err := s.events.Emit(finalCtx, turnID, "chunk", newCompletionChunk(turnID, config.ModelName, "", finish, terminalUsage)); err != nil {
		return err
	}
	return s.events.Emit(finalCtx, turnID, "completed", map[string]any{"status": "completed"})
}

func (s *ExecutionService) watchCancellation(ctx context.Context, turnID string, cancel context.CancelFunc, done <-chan struct{}) {
	ticker := time.NewTicker(cancellationPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			cancelled, err := s.turnIsCancelled(context.Background(), turnID)
			if err == nil && cancelled {
				cancel()
				return
			}
		}
	}
}

func (s *ExecutionService) turnIsCancelled(ctx context.Context, turnID string) (bool, error) {
	var status uint8
	err := s.db.WithContext(ctx).Model(&model.ChatTurn{}).Where("turn_id = ?", turnID).Select("status").Scan(&status).Error
	return status == model.TurnCancelled, err
}

func (s *ExecutionService) contextMessages(ctx context.Context, turn *model.ChatTurn, config *model.UserModelConfig, externalSessionID string) ([]ProviderMessage, error) {
	cacheBudget := int(config.MaxContextTokens) / 5
	available := int(config.MaxContextTokens) - int(config.MaxOutputTokens) - model.EstimateTokens(turn.Input) - 512
	if available < 0 {
		available = 0
	}
	if available < cacheBudget {
		cacheBudget = available
	}
	var msgs []model.ChatMessage
	if s.cache != nil {
		msgs, _ = s.cache.GetRecentMessages(ctx, externalSessionID, 0)
	}
	if len(msgs) == 0 {
		var err error
		msgs, err = model.FindRecentChatMessagesBySessionID(ctx, s.db, turn.ChatSessionID, 500)
		if err != nil {
			return nil, err
		}
		if s.cache != nil && len(msgs) > 0 {
			_ = s.cache.WarmUp(ctx, externalSessionID, msgs)
		}
	}
	out := make([]ProviderMessage, 0, len(msgs))
	tokens := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		// Failed or still-generating messages are visible in history but are
		// never fed back to the model.
		if msgs[i].Status != model.MessageStatusCompleted && msgs[i].Status != model.MessageStatusCancelled {
			continue
		}
		cost := msgs[i].EstimateTokens()
		if tokens+cost > cacheBudget {
			break
		}
		tokens += cost
		role := "assistant"
		if msgs[i].Role == model.RoleUser {
			role = "user"
		} else if msgs[i].Role == model.RoleSystem {
			role = "system"
		} else if msgs[i].Role == model.RoleTool {
			role = "tool"
		}
		if msgs[i].Content != nil {
			out = append(out, ProviderMessage{Role: role, Content: *msgs[i].Content})
		}
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	if summary, summaryErr := model.FindLatestChatSummary(ctx, s.db, turn.ChatSessionID); summaryErr == nil {
		out = append([]ProviderMessage{{Role: "system", Content: summary.Content}}, out...)
	}
	return out, nil
}

func resolveProviderBaseURL(config *model.UserModelConfig) string {
	if config.BaseURL != nil && strings.TrimSpace(*config.BaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(*config.BaseURL), "/")
	}
	if config.ProviderName == "deepseek" {
		return "https://api.deepseek.com/v1"
	}
	return "https://api.openai.com/v1"
}

func (s *ExecutionService) persistResult(
	ctx context.Context,
	turn *model.ChatTurn,
	config *model.UserModelConfig,
	externalSessionID string,
	answer string,
	usage ProviderUsage,
	finish string,
	messageStatus uint8,
	desiredTurnStatus uint8,
	errorCode string,
	errorMessage string,
) (uint8, error) {
	var userMessage *model.ChatMessage
	var assistantMessage *model.ChatMessage
	finalStatus := desiredTurnStatus
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked model.ChatTurn
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", turn.ID).First(&locked).Error; err != nil {
			return err
		}
		if locked.Status == model.TurnCompleted || locked.Status == model.TurnFailed {
			finalStatus = locked.Status
			return nil
		}
		if locked.Status == model.TurnCancelled {
			finalStatus = model.TurnCancelled
			messageStatus = model.MessageStatusCancelled
			finish = "cancelled"
			errorCode = ""
			errorMessage = ""
		}

		var existingCount int64
		if err := tx.Model(&model.ChatMessage{}).Where("turn_id = ?", turn.TurnID).Count(&existingCount).Error; err != nil {
			return err
		}
		if existingCount == 0 {
			maxSequence, err := model.GetMaxSequenceNo(ctx, tx, turn.ChatSessionID)
			if err != nil {
				return err
			}
			now := time.Now()
			input := turn.Input
			turnID := turn.TurnID
			userMessage = &model.ChatMessage{
				MessageID:     uuidgen.New(),
				ChatSessionID: turn.ChatSessionID,
				TurnID:        &turnID,
				SequenceNo:    maxSequence + 1,
				Role:          model.RoleUser,
				MessageType:   model.MessageTypeText,
				Content:       &input,
				Status:        model.MessageStatusCompleted,
				RequestID:     &turn.RequestID,
			}
			if err := model.CreateChatMessage(ctx, tx, userMessage); err != nil {
				return err
			}
			modelName := config.ModelName
			providerName := config.ProviderName
			assistantMessage = &model.ChatMessage{
				MessageID:        uuidgen.New(),
				ChatSessionID:    turn.ChatSessionID,
				TurnID:           &turnID,
				SequenceNo:       maxSequence + 2,
				Role:             model.RoleAssistant,
				MessageType:      model.MessageTypeText,
				Content:          &answer,
				ModelName:        &modelName,
				ProviderName:     &providerName,
				Status:           messageStatus,
				FinishReason:     &finish,
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				TotalTokens:      usage.TotalTokens,
				RequestID:        &turn.RequestID,
			}
			if errorCode != "" {
				assistantMessage.ErrorCode = &errorCode
				assistantMessage.ErrorMessage = &errorMessage
			}
			if err := model.CreateChatMessage(ctx, tx, assistantMessage); err != nil {
				return err
			}
			if err := model.UpdateChatSessionLastMessage(ctx, tx, turn.ChatSessionID, now); err != nil {
				return err
			}
		}

		now := time.Now()
		updates := map[string]any{"status": finalStatus, "completed_at": now}
		if errorCode != "" {
			updates["error_code"] = errorCode
			updates["error_message"] = errorMessage
		} else {
			updates["error_code"] = nil
			updates["error_message"] = nil
		}
		return tx.Model(&model.ChatTurn{}).Where("id = ?", turn.ID).Updates(updates).Error
	})
	if err != nil {
		return 0, err
	}
	if s.cache != nil && userMessage != nil && assistantMessage != nil {
		_ = s.cache.PushMessage(ctx, externalSessionID, userMessage, int64(config.MaxContextTokens/5))
		if assistantMessage.Status != model.MessageStatusFailed {
			_ = s.cache.PushMessage(ctx, externalSessionID, assistantMessage, int64(config.MaxContextTokens/5))
		}
	}
	return finalStatus, nil
}

func (s *ExecutionService) fail(ctx context.Context, turn *model.ChatTurn, modelName, code, message string) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&model.ChatTurn{}).
		Where("id = ? AND status IN ?", turn.ID, []int{int(model.TurnPending), int(model.TurnRunning)}).
		Updates(map[string]any{
			"status":        model.TurnFailed,
			"error_code":    code,
			"error_message": message,
			"completed_at":  now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return s.events.Emit(ctx, turn.TurnID, "failed", map[string]any{"status": "failed", "error_code": code, "message": message})
	}
	cancelled, err := s.turnIsCancelled(ctx, turn.TurnID)
	if err != nil {
		return err
	}
	if cancelled {
		return s.emitCancelled(ctx, turn.TurnID, modelName)
	}
	return nil
}

func (s *ExecutionService) emitCancelled(ctx context.Context, turnID, modelName string) error {
	if err := s.events.Emit(ctx, turnID, "chunk", newCompletionChunk(turnID, modelName, "", "cancelled", nil)); err != nil {
		return err
	}
	return s.events.Emit(ctx, turnID, "cancelled", map[string]any{"status": "cancelled"})
}

func isTerminalTurn(status uint8) bool {
	return status == model.TurnCompleted || status == model.TurnFailed || status == model.TurnCancelled
}
