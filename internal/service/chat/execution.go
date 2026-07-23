package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"easygo-agent/internal/credential"
	"easygo-agent/internal/model"
	redisplatform "easygo-agent/internal/platform/redis"
	"easygo-agent/internal/platform/uuidgen"
	chatcache "easygo-agent/internal/repository/chatcache"
	redisclient "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type ExecutionService struct {
	db     *gorm.DB
	cipher *credential.Cipher
	cache  *chatcache.ChatCacheRepo
	client *http.Client
}

func NewExecutionService(db *gorm.DB, cipher *credential.Cipher, cache *chatcache.ChatCacheRepo) *ExecutionService {
	return &ExecutionService{db: db, cipher: cipher, cache: cache, client: &http.Client{Timeout: 90 * time.Second}}
}

type providerMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type completionRequest struct {
	Model     string            `json:"model"`
	Messages  []providerMessage `json:"messages"`
	MaxTokens uint32            `json:"max_tokens"`
	Stream    bool              `json:"stream"`
}
type completionResponse struct {
	Choices []struct {
		Message      providerMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     uint32 `json:"prompt_tokens"`
		CompletionTokens uint32 `json:"completion_tokens"`
		TotalTokens      uint32 `json:"total_tokens"`
	} `json:"usage"`
}

func turnEventStream(turnID string) string { return "easygo:chat:turn:" + turnID + ":events" }
func emitTurnEvent(ctx context.Context, turnID, event string, data any) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return
	}
	if _, err = redisplatform.XAdd(ctx, &redisclient.XAddArgs{Stream: turnEventStream(turnID), MaxLen: 1000, Approx: true, Values: map[string]any{"event": event, "data": string(encoded)}}); err == nil {
		_, _ = redisplatform.Expire(ctx, turnEventStream(turnID), time.Hour)
	}
}

// ProcessTurn is intentionally terminal for provider failures: it records the
// sanitized failure and returns nil so the stream message can be acknowledged.
// Only persistence failures return an error and remain pending for reclaim.
func (s *ExecutionService) ProcessTurn(ctx context.Context, turnID string) error {
	turn, err := model.FindTurnInternal(ctx, s.db, turnID)
	if err != nil {
		return err
	}
	if turn.Status == model.TurnCompleted || turn.Status == model.TurnFailed || turn.Status == model.TurnCancelled {
		return nil
	}
	emitTurnEvent(ctx, turnID, "status", map[string]any{"status": "running"})
	config, err := model.FindModelConfig(ctx, s.db, turn.UserID, turn.ModelConfigID)
	if err != nil {
		return s.fail(ctx, turnID, "MODEL_CONFIG", "model configuration is unavailable")
	}
	session, err := model.FindChatSessionByID(ctx, s.db, turn.ChatSessionID)
	if err != nil {
		return s.fail(ctx, turnID, "SESSION", "chat session is unavailable")
	}
	credentialRow, err := model.FindCredential(ctx, s.db, turn.UserID, config.CredentialID)
	if err != nil {
		return s.fail(ctx, turnID, "CREDENTIAL", "model credential is unavailable")
	}
	apiKey, err := s.cipher.Decrypt(credential.Ciphertext{Ciphertext: credentialRow.Ciphertext, Nonce: credentialRow.Nonce, Algorithm: credentialRow.Algorithm, KeyVersion: credentialRow.KeyVersion}, []byte(fmt.Sprintf("user:%d:provider:%s", turn.UserID, credentialRow.ProviderName)))
	if err != nil {
		return s.fail(ctx, turnID, "CREDENTIAL", "model credential cannot be decrypted")
	}
	messages, err := s.contextMessages(ctx, turn, config, session.SessionID)
	if err != nil {
		return err
	}
	messages = append(messages, providerMessage{Role: "user", Content: turn.Input})
	answer, usage, finish, err := s.complete(ctx, config, apiKey, messages)
	if err != nil {
		return s.fail(ctx, turnID, "PROVIDER", "model request failed")
	}
	emitTurnEvent(ctx, turnID, "delta", map[string]any{"content": answer})
	if err := s.persistCompletion(ctx, turn, config, session.SessionID, answer, usage, finish); err != nil {
		return err
	}
	emitTurnEvent(ctx, turnID, "completed", map[string]any{"status": "completed"})
	return nil
}
func (s *ExecutionService) contextMessages(ctx context.Context, turn *model.ChatTurn, config *model.UserModelConfig, externalSessionID string) ([]providerMessage, error) {
	// Context history owns at most 20% of the configured window; reserve output,
	// current input and a conservative fixed prompt/tool allowance.
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
	out := make([]providerMessage, 0, len(msgs))
	tokens := 0
	for i := len(msgs) - 1; i >= 0; i-- {
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
			out = append(out, providerMessage{Role: role, Content: *msgs[i].Content})
		}
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	if summary, summaryErr := model.FindLatestChatSummary(ctx, s.db, turn.ChatSessionID); summaryErr == nil {
		out = append([]providerMessage{{Role: "system", Content: summary.Content}}, out...)
	}
	return out, nil
}
func (s *ExecutionService) complete(ctx context.Context, config *model.UserModelConfig, key string, messages []providerMessage) (string, completionResponse, string, error) {
	base := ""
	if config.BaseURL != nil {
		base = *config.BaseURL
	}
	if base == "" {
		if config.ProviderName == "deepseek" {
			base = "https://api.deepseek.com/v1"
		} else {
			base = "https://api.openai.com/v1"
		}
	}
	payload, err := json.Marshal(completionRequest{Model: config.ModelName, Messages: messages, MaxTokens: config.MaxOutputTokens})
	if err != nil {
		return "", completionResponse{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", completionResponse{}, "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", completionResponse{}, "", err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return "", completionResponse{}, "", readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", completionResponse{}, "", fmt.Errorf("provider status %d", resp.StatusCode)
	}
	var parsed completionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", completionResponse{}, "", err
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
		return "", completionResponse{}, "", fmt.Errorf("provider returned no completion")
	}
	return parsed.Choices[0].Message.Content, parsed, parsed.Choices[0].FinishReason, nil
}
func (s *ExecutionService) persistCompletion(ctx context.Context, turn *model.ChatTurn, config *model.UserModelConfig, externalSessionID, answer string, usage completionResponse, finish string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		max, err := model.GetMaxSequenceNo(ctx, tx, turn.ChatSessionID)
		if err != nil {
			return err
		}
		now := time.Now()
		input := turn.Input
		user := &model.ChatMessage{MessageID: uuidgen.New(), ChatSessionID: turn.ChatSessionID, SequenceNo: max + 1, Role: model.RoleUser, MessageType: model.MessageTypeText, Content: &input, Status: model.MessageStatusCompleted, RequestID: &turn.RequestID}
		if err := model.CreateChatMessage(ctx, tx, user); err != nil {
			return err
		}
		modelName := config.ModelName
		assistant := &model.ChatMessage{MessageID: uuidgen.New(), ChatSessionID: turn.ChatSessionID, SequenceNo: max + 2, Role: model.RoleAssistant, MessageType: model.MessageTypeText, Content: &answer, ModelName: &modelName, Status: model.MessageStatusCompleted, FinishReason: &finish, PromptTokens: usage.Usage.PromptTokens, CompletionTokens: usage.Usage.CompletionTokens, TotalTokens: usage.Usage.TotalTokens, RequestID: &turn.RequestID}
		if err := model.CreateChatMessage(ctx, tx, assistant); err != nil {
			return err
		}
		if err := model.UpdateChatSessionLastMessage(ctx, tx, turn.ChatSessionID, now); err != nil {
			return err
		}
		if err := tx.Model(&model.ChatTurn{}).Where("id = ?", turn.ID).Updates(map[string]any{"status": model.TurnCompleted, "completed_at": now}).Error; err != nil {
			return err
		}
		if s.cache != nil {
			_ = s.cache.PushMessage(ctx, externalSessionID, user, int64(config.MaxContextTokens/5))
			_ = s.cache.PushMessage(ctx, externalSessionID, assistant, int64(config.MaxContextTokens/5))
		}
		return nil
	})
}
func (s *ExecutionService) fail(ctx context.Context, turnID, code, message string) error {
	now := time.Now()
	err := s.db.WithContext(ctx).Model(&model.ChatTurn{}).Where("turn_id = ?", turnID).Updates(map[string]any{"status": model.TurnFailed, "error_code": code, "error_message": message, "completed_at": now}).Error
	if err == nil {
		emitTurnEvent(ctx, turnID, "failed", map[string]any{"status": "failed", "error_code": code, "message": message})
	}
	return err
}
