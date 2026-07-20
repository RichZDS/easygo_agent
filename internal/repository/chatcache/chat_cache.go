package chatcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/logger"
	"easygo-agent/internal/platform/redis"

	redisclient "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	// keyPrefix Redis key 统一前缀
	keyPrefix = "easygo:chat:"
)

// ======================== Lua 脚本 ========================

// trimBudgetScript 原子裁剪 Lua 脚本。
// 循环 LPOP 头部消息并 DECRBY token 计数器，直到 total <= maxTokens。
// 每行 List 格式："{tokenCount}\n{json}"，脚本解析 tokenCount 前缀做精确 DECRBY。
//
// KEYS[1]: 消息 List key
// KEYS[2]: token 计数器 key
// ARGV[1]: maxTokens（字符串）
// 返回值：弹出的消息数量
const trimBudgetScript = `
local msgKey = KEYS[1]
local tokenKey = KEYS[2]
local maxTokens = tonumber(ARGV[1])

local total = tonumber(redis.call('GET', tokenKey) or '0')
local poppedCount = 0

while total > maxTokens do
  local raw = redis.call('LPOP', msgKey)
  if not raw then
    break
  end

  -- 解析 "{tokenCount}\n{json}" 格式，取第一个 \n 之前的数字
  local sep = string.find(raw, '\n', 1, true)
  local msgTokens = 0
  if sep then
    msgTokens = tonumber(string.sub(raw, 1, sep - 1)) or 0
  end

  redis.call('DECRBY', tokenKey, msgTokens)
  total = total - msgTokens
  poppedCount = poppedCount + 1
end

return poppedCount
`

// ======================== 结构体 ========================

// Config 缓存仓储配置（从 config.yaml 读取）。
type Config struct {
	TTLSeconds  int // 滑动过期时间（秒），≤0 默认 600
	MaxMessages int // 单次获取上下文最大条数，≤0 默认 50
}

// ChatCacheRepo Redis 会话消息缓存仓储。
// 持有运行时配置（从 config.yaml 读取），使用全局 Redis 连接。
type ChatCacheRepo struct {
	ttl         time.Duration
	maxMessages int
}

// NewChatCacheRepo 创建缓存仓储。
func NewChatCacheRepo(cfg Config) *ChatCacheRepo {
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = 600
	}
	if cfg.MaxMessages <= 0 {
		cfg.MaxMessages = 50
	}
	return &ChatCacheRepo{
		ttl:         time.Duration(cfg.TTLSeconds) * time.Second,
		maxMessages: cfg.MaxMessages,
	}
}

// ======================== Key 构建 ========================

// msgKey 构建消息 List key：easygo:chat:{session_id}:msg
func msgKey(sessionID string) string {
	return keyPrefix + sessionID + ":msg"
}

// tokenKey 构建 token 计数器 key：easygo:chat:{session_id}:tokens
func tokenKey(sessionID string) string {
	return keyPrefix + sessionID + ":tokens"
}

// ======================== 读取 ========================

// GetRecentMessages 获取会话最近 count 条消息。
// 先查 Redis List；miss 或解析全部失败时返回空切片（调用方负责回源 MySQL + WarmUp 回填）。
// count ≤ 0 时使用默认值。
func (r *ChatCacheRepo) GetRecentMessages(ctx context.Context, sessionID string, count int) ([]model.ChatMessage, error) {
	if count <= 0 {
		count = r.maxMessages
	}

	key := msgKey(sessionID)

	// 检查 Redis 是否存在
	exists, err := redis.Exists(ctx, key)
	if err != nil {
		logger.Warn("redis exists check failed", zap.String("key", key), zap.Error(err))
		// 检查失败不当作 miss — 仍然尝试 LRANGE（key 可能存在）
	}

	if exists > 0 || err != nil {
		start := int64(-count)
		raw, lerr := redis.LRange(ctx, key, start, -1)
		if lerr != nil {
			logger.Warn("redis lrange failed", zap.String("key", key), zap.Error(lerr))
		} else {
			msgs := r.parseListMessages(raw)
			if len(msgs) > 0 {
				return msgs, nil
			}
		}
	}

	// 未命中：返回空，调用方负责 MySQL 回源
	return nil, nil
}

// parseListMessages 解析 Redis List 原始字符串为 ChatMessage 切片。
// 支持 "{tokenCount}\n{json}" 格式和纯 JSON 旧格式。
func (r *ChatCacheRepo) parseListMessages(raw []string) []model.ChatMessage {
	msgs := make([]model.ChatMessage, 0, len(raw))
	for _, s := range raw {
		msg := r.parseOneMessage(s)
		if msg != nil {
			msgs = append(msgs, *msg)
		}
	}
	return msgs
}

// parseOneMessage 从 Redis 存储格式解析单条消息。
// 优先按 "{tokenCount}\n{json}" 解析，失败时回退到纯 JSON。
func (r *ChatCacheRepo) parseOneMessage(raw string) *model.ChatMessage {
	// 查找第一个 \n 作为分隔符
	sepIdx := -1
	for i, b := range []byte(raw) {
		if b == '\n' {
			sepIdx = i
			break
		}
	}

	jsonStr := raw
	if sepIdx >= 0 {
		jsonStr = raw[sepIdx+1:]
	}

	var msg model.ChatMessage
	if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
		logger.Warn("unmarshal cached message failed, skip", zap.Error(err))
		return nil
	}
	return &msg
}

// WarmUp 将 MySQL 查到的消息批量回填到 Redis（miss 回源时使用）。
// 逐条 RPUSH + 设置 token 计数器 + 续期 TTL。
func (r *ChatCacheRepo) WarmUp(ctx context.Context, sessionID string, messages []model.ChatMessage) error {
	if len(messages) == 0 {
		return nil
	}

	mKey := msgKey(sessionID)
	tKey := tokenKey(sessionID)

	var totalTokens int64
	for i := range messages {
		formatted, err := formatMsgForList(&messages[i])
		if err != nil {
			logger.Warn("marshal message for warmup failed", zap.Error(err))
			continue
		}
		if _, err := redis.RPush(ctx, mKey, formatted); err != nil {
			return fmt.Errorf("warmup rpush: %w", err)
		}
		totalTokens += int64(messages[i].EstimateTokens())
	}

	// 设置 token 计数器
	if err := redis.Set(ctx, tKey, totalTokens, 0); err != nil {
		return fmt.Errorf("warmup set token count: %w", err)
	}

	// 续期 TTL（msg + tokens 两个 key）
	r.refreshTTL(ctx, mKey, tKey)

	return nil
}

// ======================== 写入 ========================

// PushMessage 写入一条消息到 Redis List 尾部，更新 token 计数器，超限裁剪，续期 TTL。
// maxTokens 为会话缓存容量上限（模型上下文的 20%）。传 0 表示不裁剪。
func (r *ChatCacheRepo) PushMessage(ctx context.Context, sessionID string, msg *model.ChatMessage, maxTokens int64) error {
	mKey := msgKey(sessionID)
	tKey := tokenKey(sessionID)

	// 1. 序列化为 "{tokenCount}\n{json}" 格式
	formatted, err := formatMsgForList(msg)
	if err != nil {
		return fmt.Errorf("format message for list: %w", err)
	}

	// 2. RPUSH 写入 List
	if _, err := redis.RPush(ctx, mKey, formatted); err != nil {
		return fmt.Errorf("rpush message: %w", err)
	}

	// 3. 估算 token 并 INCRBY
	estTokens := int64(msg.EstimateTokens())
	newTotal, err := redis.IncrBy(ctx, tKey, estTokens)
	if err != nil {
		return fmt.Errorf("incrby tokens: %w", err)
	}

	// 4. 超限裁剪（Lua 脚本原子操作）
	if maxTokens > 0 && newTotal > maxTokens {
		popped, err := r.trimToBudget(ctx, mKey, tKey, maxTokens)
		if err != nil {
			logger.Warn("trim cache failed",
				zap.String("sessionID", sessionID),
				zap.Int64("maxTokens", maxTokens),
				zap.Error(err),
			)
		} else if popped > 0 {
			logger.Debug("cache trimmed",
				zap.String("sessionID", sessionID),
				zap.Int64("popped", popped),
			)
		}
	}

	// 5. 续期 TTL
	r.refreshTTL(ctx, mKey, tKey)

	return nil
}

// trimToBudget 使用 Lua 脚本原子裁剪消息列表，直到 token 总数 ≤ maxTokens。
// 返回被弹出的消息数量。
func (r *ChatCacheRepo) trimToBudget(ctx context.Context, mKey, tKey string, maxTokens int64) (int64, error) {
	result, err := redis.Eval(ctx, trimBudgetScript, []string{mKey, tKey}, maxTokens)
	if err != nil {
		// redis.Nil 表示 key 不存在（空列表），不是错误
		if errors.Is(err, redisclient.Nil) {
			return 0, nil
		}
		return 0, fmt.Errorf("eval trim script: %w", err)
	}
	popped, _ := result.(int64)
	return popped, nil
}

// refreshTTL 同时续期 msg 和 token 两个 key 的过期时间。
func (r *ChatCacheRepo) refreshTTL(ctx context.Context, mKey, tKey string) {
	if _, err := redis.Expire(ctx, mKey, r.ttl); err != nil {
		logger.Warn("expire msg key failed", zap.String("key", mKey), zap.Error(err))
	}
	if _, err := redis.Expire(ctx, tKey, r.ttl); err != nil {
		logger.Warn("expire token key failed", zap.String("key", tKey), zap.Error(err))
	}
}

// ======================== 辅助 ========================

// GetTokenCount 获取当前缓存的 token 估算总数。
// key 不存在时返回 0（而非错误）。
func (r *ChatCacheRepo) GetTokenCount(ctx context.Context, sessionID string) (int64, error) {
	totalStr, err := redis.Get(ctx, tokenKey(sessionID))
	if err != nil {
		if errors.Is(err, redisclient.Nil) {
			return 0, nil
		}
		return 0, err
	}
	total, err := strconv.ParseInt(totalStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse token count: %w", err)
	}
	return total, nil
}

// DeleteSession 删除会话的所有 Redis 缓存。
func (r *ChatCacheRepo) DeleteSession(ctx context.Context, sessionID string) error {
	if _, err := redis.Del(ctx, msgKey(sessionID), tokenKey(sessionID)); err != nil {
		return fmt.Errorf("delete session cache: %w", err)
	}
	return nil
}

// formatMsgForList 将消息序列化为 Redis List 存储格式："{tokenCount}\n{json}"。
// tokenCount 前缀供 Lua 脚本做原子裁剪时读取，避免 TOCTOU race。
func formatMsgForList(msg *model.ChatMessage) (string, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return "", err
	}
	tokens := msg.EstimateTokens()
	// 使用 \n 分隔，JSON 中的换行会被转义为 \n，不会产生歧义
	return fmt.Sprintf("%d\n%s", tokens, string(data)), nil
}
