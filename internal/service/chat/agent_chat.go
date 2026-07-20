package chat

import (
	"context"
	"fmt"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/logger"
	chatcache "easygo-agent/internal/repository/chatcache"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ======================== 模型容量常量 ========================

// v1 只支持以下模型，会话创建时绑定模型。
const (
	ModelDeepSeekV4     = "deepseek-v4"
	ModelMiniMaxM3      = "minimax-m3"
	ModelMiniMaxM27High = "minimax-m2.7-highspeed"

	maxContextDeepSeekV4     = 1_000_000
	maxContextMiniMaxM3      = 1_000_000
	maxContextMiniMaxM27High = 204_800
)

// contextRatio Redis 缓存占模型最大上下文的比例（20%）
const contextRatio = 0.2

// GetMaxCacheTokens 根据模型名返回 Redis 缓存 token 上限（模型上下文的 20%）。
func GetMaxCacheTokens(modelName string) int64 {
	maxContext := maxContextMiniMaxM27High // 保守默认
	switch modelName {
	case ModelDeepSeekV4, ModelMiniMaxM3:
		maxContext = maxContextDeepSeekV4
	case ModelMiniMaxM27High:
		maxContext = maxContextMiniMaxM27High
	}
	return int64(float64(maxContext) * contextRatio)
}

// ======================== AgentChatService ========================

// AgentChatService Agent 对话服务。
// 负责：校验 → 获取上下文（Redis → MySQL 回源）→ 调用 LLM → 双写消息。
type AgentChatService struct {
	db        *gorm.DB
	cacheRepo *chatcache.ChatCacheRepo
	msgSvc    ChatMessageService
}

// NewAgentChatService 创建 Agent 对话服务。
func NewAgentChatService(db *gorm.DB, cacheRepo *chatcache.ChatCacheRepo, msgSvc ChatMessageService) *AgentChatService {
	return &AgentChatService{
		db:        db,
		cacheRepo: cacheRepo,
		msgSvc:    msgSvc,
	}
}

// Chat 处理一次对话：校验 → 获取上下文 → 调用 LLM → 双写消息。
// v1 只支持文本消息。
func (s *AgentChatService) Chat(ctx context.Context, userID uint64, sessionID string, modelName string, message string) (string, error) {
	// 1. 校验用户和会话
	_, err := model.FindUserByID(ctx, s.db, userID)
	if err != nil {
		logger.Warn("find user failed", zap.Uint64("userID", userID), zap.Error(err))
		return "", errorcode.New(errorcode.NotFound, "用户不存在")
	}

	session, err := model.FindChatSessionBySessionID(ctx, s.db, sessionID)
	if err != nil {
		logger.Warn("find session failed", zap.String("sessionID", sessionID), zap.Error(err))
		return "", errorcode.New(errorcode.NotFound, "会话不存在")
	}

	logger.Info("chat start",
		zap.Uint64("userID", userID),
		zap.String("sessionID", sessionID),
		zap.String("model", modelName),
	)

	// 2. 获取上下文（Redis → MySQL 回源）
	contextMsgs, err := s.loadContext(ctx, sessionID, session.ID)
	if err != nil {
		logger.Warn("load context failed", zap.Error(err))
		// 上下文获取失败不阻塞对话，降级为空上下文
	}
	logger.Info("context loaded", zap.Int("msgCount", len(contextMsgs)))

	// 3. TODO: 构建 LLM 请求 + 调用 deep agent + 获取结果
	_ = contextMsgs
	_ = message
	_ = modelName

	// 4. TODO: 保存用户消息和助手消息到 MySQL + Redis（通过 RecordMessageAndUpdate）

	return "", nil
}

// loadContext 获取会话上下文：优先 Redis，miss 时 MySQL 回源 + 回填。
func (s *AgentChatService) loadContext(ctx context.Context, sessionID string, chatSessionID uint64) ([]model.ChatMessage, error) {
	// 1. 先查 Redis
	msgs, err := s.cacheRepo.GetRecentMessages(ctx, sessionID, 0) // 0 = 使用默认 count
	if err != nil {
		logger.Warn("redis get recent messages failed", zap.Error(err))
	}

	if len(msgs) > 0 {
		return msgs, nil
	}

	// 2. Redis miss：回源 MySQL（取最近 N 条，按 sequence_no 倒序）
	mysqlMsgs, err := model.FindChatMessagesBySessionID(ctx, s.db, chatSessionID, 0, 50)
	if err != nil {
		return nil, fmt.Errorf("mysql fallback: %w", err)
	}

	// 3. 回填 Redis（best-effort）
	if len(mysqlMsgs) > 0 {
		if err := s.cacheRepo.WarmUp(ctx, sessionID, mysqlMsgs); err != nil {
			logger.Warn("cache warmup failed", zap.String("sessionID", sessionID), zap.Error(err))
		}
	}

	return mysqlMsgs, nil
}

// PushMessageToCache 将消息写入 Redis 缓存并执行容量裁剪。
// modelName 决定容量上限。由 Chat 完成 LLM 返回后调用。
func (s *AgentChatService) PushMessageToCache(ctx context.Context, sessionID string, msg *model.ChatMessage, modelName string) error {
	maxTokens := GetMaxCacheTokens(modelName)
	if err := s.cacheRepo.PushMessage(ctx, sessionID, msg, maxTokens); err != nil {
		return fmt.Errorf("push message to cache: %w", err)
	}
	return nil
}
