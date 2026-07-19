package redis

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"easygo-agent/internal/config"

	redisclient "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func Open(ctx context.Context, cfg config.Redis, log *zap.Logger) (*redisclient.Client, error) {
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
	log.Info("redis connected", zap.String("host", cfg.Host), zap.Int("port", cfg.Port), zap.Int("database", cfg.DB))
	return client, nil
}
