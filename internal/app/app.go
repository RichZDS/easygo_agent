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
	"easygo-agent/internal/taskmanager"

	"go.uber.org/zap"
)

func Run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 创建日志记录器
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

	// 创建上下文
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 创建 MySQL 连接
	db, err := mysqlplatform.Open(ctx, cfg.MySQL, log)
	if err != nil {
		return err
	}
	// 获取 MySQL 连接池
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get mysql pool: %w", err)
	}
	// 关闭 MySQL 连接池
	defer func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			log.Error("close mysql", zap.Error(closeErr))
		}
	}()

	// 创建 Redis 连接
	redisClient, err := redisplatform.Open(ctx, cfg.Redis, log)
	if err != nil {
		return err
	}
	// 关闭 Redis 连接
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			log.Error("close redis", zap.Error(closeErr))
		}
	}()

	// 创建 taskmanager 并启动任务循环
	go func() {
		taskManager := taskmanager.NewTaskManager(log)
		taskManager.RegisterBuiltinTasks()
		taskManager.Run(ctx)
	}()

	// 创建健康检查处理器
	healthHandler := handler.NewHealthHandler(map[string]handler.CheckFunc{
		"mysql": sqlDB.PingContext,
		"redis": func(ctx context.Context) error { return redisClient.Ping(ctx).Err() },
	})

	router := server.NewRouter(log, healthHandler)
	log.Info("application initialized", zap.String("name", "easygo-agent"))
	return server.Run(ctx, server.DefaultConfig(), log, router)
}
