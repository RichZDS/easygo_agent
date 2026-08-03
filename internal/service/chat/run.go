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

// RunService 负责 Agent 运行的准备（落库、占锁、组装上下文）与收尾（持久化输出、释放会话锁）。
type RunService struct {
	db        *gorm.DB
	runtime   *agentframework.RuntimeFactory
	providers *providerconfig.Service
}

func NewRunService(db *gorm.DB, runtime *agentframework.RuntimeFactory, providers *providerconfig.Service) *RunService {
	return &RunService{db: db, runtime: runtime, providers: providers}
}

// PreparedRun 是 Prepare 成功后交给 ExecutionService 的一次运行快照。
type PreparedRun struct {
	Run      *model.AgentRun
	Session  *model.Session
	Runner   *adk.TypedRunner[*schema.AgenticMessage]
	Messages []*schema.AgenticMessage
	Started  time.Time
}

// DuplicateRequestError 表示同一 user_id + request_id 的幂等重试；调用方可据此返回已有 run。
type DuplicateRequestError struct {
	Run *model.AgentRun
}

func (e *DuplicateRequestError) Error() string { return "duplicate request" }

// Prepare 为一次 Agent 运行做前置准备，返回 PreparedRun 供 ExecutionService.Run 消费。
//
// 整体分三阶段：
//  1. 事务外快速校验（参数、幂等、会话、模型、Runner）
//  2. 事务内原子落库（占锁 → 创建 run → 写用户消息 → 加载历史）
//  3. 组装 PreparedRun 快照交给 execution 层
func (s *RunService) Prepare(
	ctx context.Context,
	userID, sessionID uint64,
	requestID, input string,
) (*PreparedRun, error) {
	// ── Step 1: 参数校验 ──────────────────────────────────────────────
	input = strings.TrimSpace(input)
	requestID = strings.TrimSpace(requestID)
	if input == "" || requestID == "" {
		return nil, errorcode.New(errorcode.InvalidParameter, "input and request_id are required")
	}

	// ── Step 2: 幂等检查（事务外，快速路径） ───────────────────────────
	// 同一 user_id + request_id 重复提交时直接返回 DuplicateRequestError，
	// 调用方（controller）可据此返回已有 run，避免重复执行。
	if existing, err := model.FindAgentRunByUserRequest(ctx, s.db, userID, requestID); err == nil {
		return nil, &DuplicateRequestError{Run: existing}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errorcode.Wrap(errorcode.Database, err)
	}

	// ── Step 3: 会话校验 ──────────────────────────────────────────────
	// 确认 session 归属当前用户，且已绑定模型、当前无进行中的 run。
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

	// ── Step 4: 解析模型配置 ────────────────────────────────────────────
	// 从 DB 读取 provider / API Key / model_id 等，解密后组装 ModelSpec。
	spec, aiModel, _, err := s.providers.ResolveModelSpec(ctx, userID, *session.CurrentAIModelID)
	if err != nil {
		return nil, err
	}

	// ── Step 5: 构建 Eino Runner（事务外，避免长事务） ─────────────────
	// Registry.Build → AgenticModel → TypedChatModelAgent → TypedRunner。
	// Runner 在事务外创建：HTTP 客户端初始化不涉及 DB，且避免持锁期间做网络 IO。
	runner, err := s.runtime.Build(ctx, userID, spec)
	if err != nil {
		return nil, errorcode.Wrap(errorcode.InvalidParameter, fmt.Errorf("build TypedRunner: %w", err))
	}

	// ── Step 6: 事务内原子落库 ─────────────────────────────────────────
	var prepared *PreparedRun
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Step 6.1: SELECT ... FOR UPDATE 行锁 session，串行化同 session 的并发请求。
		locked, err := model.LockSession(ctx, tx, sessionID, userID)
		if err != nil {
			return err
		}

		// Step 6.2: 持锁后二次校验——Step 3 到此处之间可能有人切换了模型或发起了另一 run。
		if locked.CurrentAIModelID == nil || *locked.CurrentAIModelID != aiModel.ID {
			return errorcode.New(errorcode.Conflict, "session model changed; retry the request")
		}
		if locked.ActiveRunID != nil {
			return errorcode.New(errorcode.Conflict, "session already has an active run")
		}

		// Step 6.3: 持锁后再查 request_id，消除 Step 2 与 Step 6 之间的 TOCTOU 窗口。
		if existing, findErr := model.FindAgentRunByUserRequest(ctx, tx, userID, requestID); findErr == nil {
			return &DuplicateRequestError{Run: existing}
		} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}

		// Step 6.4: 创建 agent_run 记录，快照本次运行的 provider / model / instruction 配置。
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

		// Step 6.5: 将会话 active_run_id 指向新 run，阻止同 session 并发执行。
		if err := model.ClaimSessionRun(ctx, tx, sessionID, run.ID); err != nil {
			return err
		}

		// Step 6.6: 持久化用户消息，分配单调递增的 message_seq。
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

		// Step 6.7: 加载最近 N 条会话历史（含刚写入的用户消息），转为 Eino AgenticMessage。
		// N 由 runtime.ContextMessageLimit() 决定，控制 LLM 上下文窗口大小。
		history, err := model.ListRecentMessages(ctx, tx, sessionID, s.runtime.ContextMessageLimit())
		if err != nil {
			return err
		}
		// TODO: 按 model max_context_length 做 token 级压缩。
		history, err = CompactHistory(history, s.runtime.ContextMessageLimit())
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

		// Step 6.8: 组装 PreparedRun 快照，事务提交后交给 ExecutionService.Run。
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

// Finalize 在事务内写入 assistant 输出、更新 run 终态与 token 统计，并释放 session 的 active run 占用。
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
			// 批量插入 assistant 消息，避免循环内逐条写库。
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

// CreateSession 创建会话并绑定默认 AI 模型（模型需已通过 ResolveModelSpec 校验）。
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

// truncate 截断 error_message 等可变长文本，避免超出 DB 列宽。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
