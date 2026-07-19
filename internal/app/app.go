package app

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"easygo-agent/internal/agent/callback"
	"easygo-agent/internal/config"
	"easygo-agent/internal/controller"
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

	// 初始化全局日志（之后任意包通过 logger.L() 使用）
	if err := logger.Init(logger.Config{
		Environment: "development",
		Level:       "info",
		File:        "logs/app.log",
		MaxSizeMB:   100,
		MaxBackups:  10,
		MaxAgeDays:  30,
		Compress:    true,
	}); err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	// 创建上下文
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 创建 MySQL 连接
	db, err := mysqlplatform.Open(ctx, cfg.MySQL)
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
			logger.Error("close mysql", zap.Error(closeErr))
		}
	}()

	// 创建 Redis 连接
	redisClient, err := redisplatform.Open(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	// 关闭 Redis 连接
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			logger.Error("close redis", zap.Error(closeErr))
		}
	}()

	// 创建 taskmanager 并启动任务循环
	go func() {
		taskManager := taskmanager.NewTaskManager()
		taskManager.RegisterBuiltinTasks()
		taskManager.Run(ctx)
	}()

	// 初始化回调处理器
	callback.Init()
	// 创建健康检查处理器
	healthHandler := handler.NewHealthHandler(map[string]handler.CheckFunc{
		"mysql": sqlDB.PingContext,
		"redis": func(ctx context.Context) error { return redisClient.Ping(ctx).Err() },
	})

	// 初始化所有 Controller（内部自动创建 Service）
	ctls := controller.InitAll(db)

	router := server.NewRouter(healthHandler, ctls.User, ctls.Session, ctls.Message)
	logger.Info("application initialized", zap.String("name", "easygo-agent"))
	return server.Run(ctx, server.DefaultConfig(), router)
}
