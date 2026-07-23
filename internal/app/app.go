package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"easygo-agent/internal/agent/callback"
	"easygo-agent/internal/auth"
	"easygo-agent/internal/config"
	"easygo-agent/internal/credential"
	"easygo-agent/internal/handler"
	"easygo-agent/internal/platform/logger"
	mysqlplatform "easygo-agent/internal/platform/mysql"
	redisplatform "easygo-agent/internal/platform/redis"
	chatcache "easygo-agent/internal/repository/chatcache"
	"easygo-agent/internal/server"
	"easygo-agent/internal/service/chat"
	"easygo-agent/internal/taskmanager"
	"easygo-agent/internal/wire"

	"go.uber.org/zap"
)

func Run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	issuer, err := auth.NewIssuer(os.Getenv("EASYGO_JWT_HS256_SECRET"), auth.DefaultTTL)
	if err != nil {
		return fmt.Errorf("configure JWT: %w", err)
	}
	cipher, err := credential.NewFromBase64(os.Getenv("EASYGO_CREDENTIAL_KEK_V1"), "v1")
	if err != nil {
		return fmt.Errorf("configure credential encryption: %w", err)
	}

	// 初始化全局日志（之后任意包通过 logger.L() 使用）
	if err := logger.Init(logger.Config{
		Environment: cfg.Logger.Environment,
		Level:       cfg.Logger.Level,
		File:        cfg.Logger.File,
		MaxSizeMB:   cfg.Logger.MaxSizeMB,
		MaxBackups:  cfg.Logger.MaxBackups,
		MaxAgeDays:  cfg.Logger.MaxAgeDays,
		Compress:    cfg.Logger.Compress,
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

	// 初始化全局 Redis 连接
	if err := redisplatform.Init(ctx, cfg.Redis); err != nil {
		return err
	}
	// 关闭 Redis 连接
	defer func() {
		if closeErr := redisplatform.Close(); closeErr != nil {
			logger.Error("close redis", zap.Error(closeErr))
		}
	}()

	// The legacy scheduler remains available for periodic maintenance. The
	// Stream worker owns durable outbox publication; processing is attached with
	// the provider executor in the next batch.
	go func() {
		taskManager := taskmanager.NewTaskManager()
		taskManager.RegisterBuiltinTasks()
		taskManager.Run(ctx)
	}()
	go taskmanager.NewStreamWorker(
		chat.NewTurnService(db),
		chat.NewExecutionService(db, cipher, chatcache.NewChatCacheRepo(chatcache.Config{TTLSeconds: cfg.Redis.CacheTTLSeconds, MaxMessages: cfg.Redis.ContextMaxMessages})),
	).Run(ctx)

	// 初始化回调处理器
	callback.Init()
	// 创建健康检查处理器
	healthHandler := handler.NewHealthHandler(map[string]handler.CheckFunc{
		"mysql": sqlDB.PingContext,
		"redis": func(ctx context.Context) error { return redisplatform.L().Ping(ctx).Err() },
	})

	// 使用 Wire 依赖注入创建所有 Controller
	ctls := wire.InitControllers(db, chatcache.Config{
		TTLSeconds:  cfg.Redis.CacheTTLSeconds,
		MaxMessages: cfg.Redis.ContextMaxMessages,
	}, cipher, issuer)

	router := server.NewRouter(healthHandler, ctls.User, ctls.Session, ctls.Message, ctls.Auth, ctls.ModelConfig, ctls.Turn, issuer)
	logger.Info("application initialized", zap.String("name", "easygo-agent"))
	return server.Run(ctx, server.DefaultConfig(), router)
}
