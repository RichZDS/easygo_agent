package redis

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"easygo-agent/internal/config"
	"easygo-agent/internal/platform/logger"

	redisclient "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

var (
	mu     sync.RWMutex
	global *redisclient.Client
)

// Init 初始化全局 Redis 客户端，整个程序共用一份。启动时调用一次即可。
func Init(ctx context.Context, cfg config.Redis) error {
	client, err := Open(ctx, cfg)
	if err != nil {
		return err
	}
	mu.Lock()
	global = client
	mu.Unlock()
	return nil
}

// L 返回全局 *redis.Client，需要链式调用或非标准操作时使用。
func L() *redisclient.Client {
	mu.RLock()
	defer mu.RUnlock()
	return global
}

// Close 关闭全局 Redis 连接。
func Close() error {
	mu.RLock()
	client := global
	mu.RUnlock()
	if client == nil {
		return nil
	}
	return client.Close()
}

// Open 创建并验证一个新的 Redis 客户端，不设置全局变量。
// 一般业务直接用 Init；需要多个 Redis 实例时调用 Open。
func Open(ctx context.Context, cfg config.Redis) (*redisclient.Client, error) {
	client := redisclient.NewClient(&redisclient.Options{
		Addr:         net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	logger.Info("redis connected", zap.String("host", cfg.Host), zap.Int("port", cfg.Port), zap.Int("database", cfg.DB))
	return client, nil
}

// ======================== 包级便捷方法 ========================

// Get 读取一个 key 的值。
func Get(ctx context.Context, key string) (string, error) {
	return L().Get(ctx, key).Result()
}

// Set 写入一个 key，支持过期时间。expiration 为 0 表示永不过期。
func Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	return L().Set(ctx, key, value, expiration).Err()
}

// SetNX 仅当 key 不存在时写入，常用于分布式锁。
func SetNX(ctx context.Context, key string, value any, expiration time.Duration) (bool, error) {
	return L().SetNX(ctx, key, value, expiration).Result()
}

// Del 删除一个或多个 key，返回被删除的数量。
func Del(ctx context.Context, keys ...string) (int64, error) {
	return L().Del(ctx, keys...).Result()
}

// Exists 检查 key 是否存在，返回存在的数量。
func Exists(ctx context.Context, keys ...string) (int64, error) {
	return L().Exists(ctx, keys...).Result()
}

// Expire 设置 key 的过期时间。
func Expire(ctx context.Context, key string, expiration time.Duration) (bool, error) {
	return L().Expire(ctx, key, expiration).Result()
}

// Incr 自增 1。
func Incr(ctx context.Context, key string) (int64, error) {
	return L().Incr(ctx, key).Result()
}

// Decr 自减 1。
func Decr(ctx context.Context, key string) (int64, error) {
	return L().Decr(ctx, key).Result()
}

// ======================== Hash 操作 ========================

// HGet 获取 hash 中 field 的值。
func HGet(ctx context.Context, key, field string) (string, error) {
	return L().HGet(ctx, key, field).Result()
}

// HSet 设置 hash 中 field 的值。
func HSet(ctx context.Context, key string, values ...any) (int64, error) {
	return L().HSet(ctx, key, values...).Result()
}

// HGetAll 获取 hash 中所有 field 和 value。
func HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return L().HGetAll(ctx, key).Result()
}

// HDel 删除 hash 中的一个或多个 field。
func HDel(ctx context.Context, key string, fields ...string) (int64, error) {
	return L().HDel(ctx, key, fields...).Result()
}

// ======================== List 操作 ========================

// LPush 向列表左侧插入元素。
func LPush(ctx context.Context, key string, values ...any) (int64, error) {
	return L().LPush(ctx, key, values...).Result()
}

// RPush 向列表右侧插入元素。
func RPush(ctx context.Context, key string, values ...any) (int64, error) {
	return L().RPush(ctx, key, values...).Result()
}

// LRange 获取列表指定范围的元素。
func LRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	return L().LRange(ctx, key, start, stop).Result()
}

// LLen 获取列表长度。
func LLen(ctx context.Context, key string) (int64, error) {
	return L().LLen(ctx, key).Result()
}
