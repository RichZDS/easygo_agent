package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agentframework "easygo-agent/internal/agent"
	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/service/providerconfig"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
)

// RunService prepares and finalizes agent runs.
type RunService struct {
	db        *gorm.DB
	runtime   *agentframework.RuntimeFactory
	providers *providerconfig.Service
}

func NewRunService(db *gorm.DB, runtime *agentframework.RuntimeFactory, providers *providerconfig.Service) *RunService {
	return &RunService{db: db, runtime: runtime, providers: providers}
}

type PreparedRun struct {
	Run      *model.AgentRun
	Session  *model.Session
	Runner   *adk.TypedRunner[*schema.AgenticMessage]
	Messages []*schema.AgenticMessage
	Started  time.Time
}

type DuplicateRequestError struct {
	Run *model.AgentRun
}

func (e *DuplicateRequestError) Error() string { return "duplicate request" }

func (s *RunService) Prepare(
	ctx context.Context,
	userID, sessionID uint64,
	requestID, input string,
) (*PreparedRun, error) {
	input = strings.TrimSpace(input)
	requestID = strings.TrimSpace(requestID)
	if input == "" || requestID == "" {
		return nil, errorcode.New(errorcode.InvalidParameter, "input and request_id are required")
	}

	if existing, err := model.FindAgentRunByUserRequest(ctx, s.db, userID, requestID); err == nil {
		return nil, &DuplicateRequestError{Run: existing}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errorcode.Wrap(errorcode.Database, err)
	}

	session, err := model.FindSessionOwnedByUser(ctx, s.db, sessionID, userID)
	if err != nil {
		return nil, err
	}
	if session.CurrentAIModelID == nil {
		return nil, errorcode.New(errorcode.InvalidParameter, "session model is not configured")
	}
	if session.ActiveRunID != nil {
		return nil, errorcode.New(errorcode.Conflict, "session already has an active run")
	}

	spec, aiModel, _, err := s.providers.ResolveModelSpec(ctx, userID, *session.CurrentAIModelID)
	if err != nil {
		return nil, err
	}
	runner, err := s.runtime.Build(ctx, spec)
	if err != nil {
		return nil, errorcode.Wrap(errorcode.InvalidParameter, fmt.Errorf("build TypedRunner: %w", err))
	}

	var prepared *PreparedRun
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := model.LockSession(ctx, tx, sessionID, userID)
		if err != nil {
			return err
		}
		if locked.CurrentAIModelID == nil || *locked.CurrentAIModelID != aiModel.ID {
			return errorcode.New(errorcode.Conflict, "session model changed; retry the request")
		}
		if locked.ActiveRunID != nil {
			return errorcode.New(errorcode.Conflict, "session already has an active run")
		}
		if existing, findErr := model.FindAgentRunByUserRequest(ctx, tx, userID, requestID); findErr == nil {
			return &DuplicateRequestError{Run: existing}
		} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}

		now := time.Now()
		run := &model.AgentRun{
			SessionID: sessionID,
			UserID:    userID,
			RequestID: requestID,
			AIModelID: aiModel.ID,
			Status:    model.AgentRunRunning,
			ConfigSnapshot: model.JSONMap{
				"provider":    spec.Provider,
				"model_id":    aiModel.ModelID,
				"instruction": agentframework.DefaultInstruction,
				"context_n":   s.runtime.ContextMessageLimit(),
			},
			StartedAt: &now,
		}
		if err := model.CreateAgentRun(ctx, tx, run); err != nil {
			return err
		}
		if err := model.ClaimSessionRun(ctx, tx, sessionID, run.ID); err != nil {
			return err
		}

		userMsg := schema.UserAgenticMessage(input)
		seq, err := model.AllocateMessageSeq(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		runID := run.ID
		row, err := model.NewChatMessageFromAgentic(sessionID, &runID, seq, userMsg)
		if err != nil {
			return err
		}
		if err := model.CreateChatMessage(ctx, tx, row); err != nil {
			return err
		}
		if err := model.BumpSessionMessageStats(ctx, tx, sessionID, 1, now); err != nil {
			return err
		}

		history, err := model.ListRecentMessages(ctx, tx, sessionID, s.runtime.ContextMessageLimit())
		if err != nil {
			return err
		}
		messages := make([]*schema.AgenticMessage, 0, len(history))
		for i := range history {
			msg, convErr := history[i].ToAgenticMessage()
			if convErr != nil {
				return errorcode.Wrap(errorcode.Internal, convErr)
			}
			messages = append(messages, msg)
		}

		prepared = &PreparedRun{
			Run:      run,
			Session:  locked,
			Runner:   runner,
			Messages: messages,
			Started:  now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

type FinalizeInput struct {
	Outputs          []*schema.AgenticMessage
	Status           uint8
	ErrorCode        string
	ErrorMessage     string
	PromptTokens     *uint32
	CompletionTokens *uint32
	TotalTokens      *uint32
}

func (s *RunService) Finalize(ctx context.Context, prepared *PreparedRun, in FinalizeInput) error {
	if prepared == nil || prepared.Run == nil {
		return fmt.Errorf("prepared run is required")
	}
	now := time.Now()
	latency := uint32(now.Sub(prepared.Started).Milliseconds())

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if in.Status == model.AgentRunSucceeded && len(in.Outputs) > 0 {
			rows := make([]*model.ChatMessage, 0, len(in.Outputs))
			runID := prepared.Run.ID
			for _, out := range in.Outputs {
				if out == nil {
					continue
				}
				seq, err := model.AllocateMessageSeq(ctx, tx, prepared.Run.SessionID)
				if err != nil {
					return err
				}
				row, err := model.NewChatMessageFromAgentic(prepared.Run.SessionID, &runID, seq, out)
				if err != nil {
					return err
				}
				rows = append(rows, row)
			}
			if err := model.CreateChatMessages(ctx, tx, rows); err != nil {
				return err
			}
			if len(rows) > 0 {
				if err := model.BumpSessionMessageStats(ctx, tx, prepared.Run.SessionID, len(rows), now); err != nil {
					return err
				}
			}
		}

		updates := map[string]any{
			"status":        in.Status,
			"latency_ms":    latency,
			"finished_at":   now,
			"error_code":    in.ErrorCode,
			"error_message": truncate(in.ErrorMessage, 1000),
		}
		if in.PromptTokens != nil {
			updates["prompt_tokens"] = *in.PromptTokens
		}
		if in.CompletionTokens != nil {
			updates["completion_tokens"] = *in.CompletionTokens
		}
		if in.TotalTokens != nil {
			updates["total_tokens"] = *in.TotalTokens
		}
		if err := model.FinalizeAgentRun(ctx, tx, prepared.Run.ID, updates); err != nil {
			return err
		}
		return model.ClearSessionActiveRun(ctx, tx, prepared.Run.SessionID, prepared.Run.ID)
	})
}

func (s *RunService) GetRun(ctx context.Context, userID, runID uint64) (*model.AgentRun, error) {
	return model.FindAgentRunOwnedByUser(ctx, s.db, userID, runID)
}

func (s *RunService) CreateSession(ctx context.Context, userID uint64, title string, aiModelID uint64) (*model.Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新对话"
	}
	if _, _, _, err := s.providers.ResolveModelSpec(ctx, userID, aiModelID); err != nil {
		return nil, err
	}
	row := &model.Session{
		UserID:           userID,
		Title:            title,
		Status:           model.SessionStatusNormal,
		CurrentAIModelID: &aiModelID,
		NextMessageSeq:   1,
	}
	if err := model.CreateSession(ctx, s.db, row); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *RunService) ListSessions(ctx context.Context, userID uint64) ([]model.Session, error) {
	return model.ListSessionsByUser(ctx, s.db, userID)
}

func (s *RunService) ListMessages(ctx context.Context, userID, sessionID uint64, limit int) ([]model.ChatMessage, error) {
	if _, err := model.FindSessionOwnedByUser(ctx, s.db, sessionID, userID); err != nil {
		return nil, err
	}
	return model.ListMessagesBySession(ctx, s.db, sessionID, limit)
}

func (s *RunService) SwitchModel(ctx context.Context, userID, sessionID, aiModelID uint64) error {
	if _, err := model.FindSessionOwnedByUser(ctx, s.db, sessionID, userID); err != nil {
		return err
	}
	if _, _, _, err := s.providers.ResolveModelSpec(ctx, userID, aiModelID); err != nil {
		return err
	}
	return model.UpdateSessionModel(ctx, s.db, sessionID, aiModelID)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
