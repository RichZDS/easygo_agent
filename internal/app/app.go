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
	"easygo-agent/internal/platform/auth"
	"easygo-agent/internal/platform/handler"
	"easygo-agent/internal/platform/logger"
	mysqlplatform "easygo-agent/internal/platform/mysql"
	"easygo-agent/internal/server"
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
	defer func() { _ = logger.Sync() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := mysqlplatform.Open(ctx, cfg.MySQL)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get mysql pool: %w", err)
	}
	defer func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			logger.Error("close mysql", zap.Error(closeErr))
		}
	}()

	callback.Init()
	runtime := agentframework.NewRuntimeFactory(agentframework.NewRegistry())
	healthHandler := handler.NewHealthHandler(map[string]handler.CheckFunc{
		"mysql": sqlDB.PingContext,
	})
	controllers := wire.InitControllers(db, cipher, issuer, runtime)
	router := server.NewRouter(
		healthHandler,
		controllers.User,
		controllers.Auth,
		controllers.Provider,
		controllers.Chat,
		issuer,
	)
	logger.Info("application initialized", zap.String("name", "easygo-agent"))
	return server.Run(ctx, server.DefaultConfig(), router)
}
