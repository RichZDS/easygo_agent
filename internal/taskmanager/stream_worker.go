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

const streamGroup = "easygo-chat-workers"

// TurnProcessor is implemented by the provider execution batch. Returning an
// error leaves the message pending for XAUTOCLAIM; it is never silently lost.
type TurnProcessor interface {
	ProcessTurn(context.Context, string) error
}

type StreamWorker struct {
	turns     *chat.TurnService
	processor TurnProcessor
	consumer  string
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

// Run owns the outbox publisher and consumes at most one entry per user per
// cycle. The NX lease also preserves per-user ordering across application
// replicas.
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
func (w *StreamWorker) publish(ctx context.Context) error {
	return w.turns.PublishPending(ctx, func(ctx context.Context, userID uint64, turnID string) (string, error) {
		return redisplatform.XAdd(ctx, &redisclient.XAddArgs{Stream: streamName(userID), Values: map[string]any{"turn_id": turnID}})
	})
}
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
func (w *StreamWorker) consumeUser(ctx context.Context, userID uint64) error {
	locked, err := redisplatform.SetNX(ctx, leaseKey(userID), w.consumer, 2*time.Minute)
	if err != nil || !locked {
		return err
	}
	defer func() { _, _ = redisplatform.Del(context.Background(), leaseKey(userID)) }()
	stream := streamName(userID)
	err = redisplatform.XGroupCreateMkStream(ctx, stream, streamGroup, "0")
	if err != nil && !stringsContains(err.Error(), "BUSYGROUP") {
		return err
	}
	messages, _, claimErr := redisplatform.XAutoClaim(ctx, &redisclient.XAutoClaimArgs{Stream: stream, Group: streamGroup, Consumer: w.consumer, MinIdle: 30 * time.Second, Start: "0-0", Count: 1})
	if claimErr != nil && !errors.Is(claimErr, redisclient.Nil) {
		return claimErr
	}
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
			_, _ = redisplatform.XAck(ctx, stream, streamGroup, message.ID)
			continue
		}
		if err := w.turns.MarkRunning(ctx, turnID); err != nil {
			return err
		}
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

// Ensure the status constants are retained in this package's documented queue contract.
var _ = model.TurnRunning
