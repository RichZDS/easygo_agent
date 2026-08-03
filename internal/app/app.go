package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	agentframework "easygo-agent/internal/agent"
	"easygo-agent/internal/agent/callback"
	"easygo-agent/internal/config"
	"easygo-agent/internal/credential"
	"easygo-agent/internal/cronjob"
	"easygo-agent/internal/framejob"
	"easygo-agent/internal/platform/auth"
	"easygo-agent/internal/platform/handler"
	"easygo-agent/internal/platform/logger"
	mysqlplatform "easygo-agent/internal/platform/mysql"
	"easygo-agent/internal/server"
	skillstore "easygo-agent/internal/skill/store"
	skillsync "easygo-agent/internal/skill/sync"
	"easygo-agent/internal/skill/workspace"
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
	// syncLogger flushes buffered structured log entries during shutdown.
	defer func() { _ = logger.Sync() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	workspaceManager, err := workspace.NewManager(cfg.Skills)
	if err != nil {
		logger.ErrorContext(ctx, "initialize skill workspace failed", zap.Error(err))
		return fmt.Errorf("initialize skill workspace: %w", err)
	}
	syncer, err := skillsync.New(cfg.Skills)
	if err != nil {
		logger.ErrorContext(ctx, "initialize builtin skill sync failed", zap.Error(err))
		return fmt.Errorf("initialize builtin skill sync: %w", err)
	}
	if err := syncer.Sync(ctx); err != nil {
		logger.ErrorContext(ctx, "synchronize builtin skills failed", zap.Error(err))
		return fmt.Errorf("synchronize builtin skills: %w", err)
	}
	skillStore, err := skillstore.New(cfg.Skills, workspaceManager)
	if err != nil {
		logger.ErrorContext(ctx, "initialize skill store failed", zap.Error(err))
		return fmt.Errorf("initialize skill store: %w", err)
	}
	if err := skillStore.CleanupStaging(ctx); err != nil {
		logger.ErrorContext(ctx, "clean skill staging directory failed", zap.Error(err))
		return fmt.Errorf("clean skill staging directory: %w", err)
	}

	db, err := mysqlplatform.Open(ctx, cfg.MySQL)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get mysql pool: %w", err)
	}
	// closeSQLPool releases database connections and records shutdown failures.
	defer func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			logger.Error("close mysql", zap.Error(closeErr))
		}
	}()

	callback.Init()

	cronMgr := cronjob.NewManager(db)
	cronjob.RegisterBuiltinCronJobs(cronMgr)
	if err := cronMgr.Start(ctx); err != nil {
		return fmt.Errorf("start cronjob manager: %w", err)
	}
	defer cronMgr.Stop()

	frameMgr := framejob.NewManager()
	framejob.RegisterBuiltinFrameJobs(frameMgr)
	frameMgr.Start(ctx)
	defer frameMgr.Stop()

	runtime := agentframework.NewRuntimeFactory(agentframework.NewRegistry(), workspaceManager)
	healthHandler := handler.NewHealthHandler(map[string]handler.CheckFunc{
		"mysql": sqlDB.PingContext,
	})
	controllers := wire.InitControllers(db, cipher, issuer, runtime, skillStore)
	router := server.NewRouter(
		healthHandler,
		controllers.User,
		controllers.Auth,
		controllers.Provider,
		controllers.Chat,
		controllers.Skill,
		issuer,
	)
	logger.Info("application initialized", zap.String("name", "easygo-agent"))
	return server.Run(ctx, server.DefaultConfig(), router)
}
