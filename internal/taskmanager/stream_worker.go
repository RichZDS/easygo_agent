package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/logger"
	redisplatform "easygo-agent/internal/platform/redis"
	"easygo-agent/internal/service/chat"

	redisclient "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// streamGroup 是 Redis Stream 消费者组名，同一组内消息只被消费一次。
const streamGroup = "easygo-chat-workers"

// TurnProcessor 由 ExecutionService 实现，负责实际执行一轮对话。
// 返回 error 时消息保持 pending，后续由 XAUTOCLAIM 重试，不会静默丢失。
type TurnProcessor interface {
	ProcessTurn(context.Context, string) error
}

// StreamWorker 将 DB outbox 发布到 Redis Stream，并按用户串行消费 turn。
type StreamWorker struct {
	turns     *chat.TurnService
	processor TurnProcessor
	consumer  string // 消费者标识：hostname-pid，用于 XREADGROUP / XAUTOCLAIM
}

func NewStreamWorker(turns *chat.TurnService, processor TurnProcessor) *StreamWorker {
	return &StreamWorker{turns: turns, processor: processor, consumer: fmt.Sprintf("%s-%d", hostname(), os.Getpid())}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "worker"
	}
	return h
}

func streamName(userID uint64) string { return fmt.Sprintf("easygo:chat:user:%d", userID) }
func leaseKey(userID uint64) string   { return fmt.Sprintf("easygo:chat:user:%d:lease", userID) }

// Run 主循环：先发布 outbox，再消费 Stream。
// 每轮每个用户最多处理一条消息；NX 租约保证多副本下同一用户仍按序执行。
func (w *StreamWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.publish(ctx); err != nil {
				logger.Warn("publish chat outbox", zap.Error(err))
			}
			if w.processor == nil {
				continue
			}
			if err := w.consume(ctx); err != nil {
				logger.Warn("consume chat stream", zap.Error(err))
			}
		}
	}
}

// publish 扫描 pending outbox，写入对应用户的 Redis Stream 并标记为 published。
func (w *StreamWorker) publish(ctx context.Context) error {
	return w.turns.PublishPending(ctx, func(ctx context.Context, userID uint64, turnID string) (string, error) {
		return redisplatform.XAdd(ctx, &redisclient.XAddArgs{Stream: streamName(userID), Values: map[string]any{"turn_id": turnID}})
	})
}

// consume 遍历仍有 published 消息的用户，逐个尝试消费。
func (w *StreamWorker) consume(ctx context.Context) error {
	users, err := w.turns.PublishedUserIDs(ctx)
	if err != nil {
		return err
	}
	for _, userID := range users {
		if err := w.consumeUser(ctx, userID); err != nil {
			return err
		}
	}
	return nil
}

// consumeUser 在持有用户租约的前提下，从 Stream 取最多一条消息并执行。
func (w *StreamWorker) consumeUser(ctx context.Context, userID uint64) error {
	// 租约：同一时刻只有一个 worker 处理该用户，避免多副本乱序。
	locked, err := redisplatform.SetNX(ctx, leaseKey(userID), w.consumer, 2*time.Minute)
	if err != nil || !locked {
		return err
	}
	defer func() { _, _ = redisplatform.Del(context.Background(), leaseKey(userID)) }()

	stream := streamName(userID)
	// 消费者组幂等创建；已存在时 Redis 返回 BUSYGROUP，可忽略。
	err = redisplatform.XGroupCreateMkStream(ctx, stream, streamGroup, "0")
	if err != nil && !stringsContains(err.Error(), "BUSYGROUP") {
		return err
	}

	// 优先认领 idle 超过 30s 的 pending 消息（上次执行失败或 worker 崩溃）。
	messages, _, claimErr := redisplatform.XAutoClaim(ctx, &redisclient.XAutoClaimArgs{Stream: stream, Group: streamGroup, Consumer: w.consumer, MinIdle: 30 * time.Second, Start: "0-0", Count: 1})
	if claimErr != nil && !errors.Is(claimErr, redisclient.Nil) {
		return claimErr
	}
	// 无待认领消息时，读取一条新消息（">" 表示仅未投递给本组的消息）。
	if len(messages) == 0 {
		streams, readErr := redisplatform.XReadGroup(ctx, &redisclient.XReadGroupArgs{Group: streamGroup, Consumer: w.consumer, Streams: []string{stream, ">"}, Count: 1, Block: 5 * time.Millisecond})
		if readErr != nil && !errors.Is(readErr, redisclient.Nil) {
			return readErr
		}
		for _, s := range streams {
			messages = append(messages, s.Messages...)
		}
	}

	for _, message := range messages {
		turnID, _ := message.Values["turn_id"].(string)
		if turnID == "" {
			// 脏消息直接 ACK，避免阻塞队列。
			_, _ = redisplatform.XAck(ctx, stream, streamGroup, message.ID)
			continue
		}
		if err := w.turns.MarkRunning(ctx, turnID); err != nil {
			return err
		}
		// ProcessTurn 失败时不 ACK，消息留 pending 等待 XAUTOCLAIM 重试。
		if err := w.processor.ProcessTurn(ctx, turnID); err != nil {
			return err
		}
		if _, err := redisplatform.XAck(ctx, stream, streamGroup, message.ID); err != nil {
			return err
		}
	}
	return nil
}

func stringsContains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// 保留 TurnRunning 常量引用，确保队列状态契约在本包文档化后不被误删。
var _ = model.TurnRunning
