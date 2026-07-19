package app

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"easygo-agent/internal/config"
	"easygo-agent/internal/handler"
	"easygo-agent/internal/platform/logger"
	mysqlplatform "easygo-agent/internal/platform/mysql"
	redisplatform "easygo-agent/internal/platform/redis"
	"easygo-agent/internal/server"

	"go.uber.org/zap"
)

func Run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log, err := logger.New(logger.Config{
		Environment: "development",
		Level:       "info",
		File:        "logs/app.log",
		MaxSizeMB:   100,
		MaxBackups:  10,
		MaxAgeDays:  30,
		Compress:    true,
	})
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}
	defer func() { _ = log.Sync() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := mysqlplatform.Open(ctx, cfg.MySQL, log)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get mysql pool: %w", err)
	}
	defer func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			log.Error("close mysql", zap.Error(closeErr))
		}
	}()

	redisClient, err := redisplatform.Open(ctx, cfg.Redis, log)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			log.Error("close redis", zap.Error(closeErr))
		}
	}()

	healthHandler := handler.NewHealthHandler(map[string]handler.CheckFunc{
		"mysql": sqlDB.PingContext,
		"redis": func(ctx context.Context) error { return redisClient.Ping(ctx).Err() },
	})

	router := server.NewRouter(log, healthHandler)
	log.Info("application initialized", zap.String("name", "easygo-agent"))
	return server.Run(ctx, server.DefaultConfig(), log, router)
}
