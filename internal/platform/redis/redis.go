package redis

import (
	"context"
	"fmt"

	"easygo-agent/internal/config"

	redisclient "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func Open(ctx context.Context, cfg config.Redis, log *zap.Logger) (*redisclient.Client, error) {
	client := redisclient.NewClient(&redisclient.Options{
		Addr:         cfg.Address,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	log.Info("redis connected", zap.String("address", cfg.Address), zap.Int("database", cfg.DB))
	return client, nil
}
