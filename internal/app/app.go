package app

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"easygo-agent/internal/config"
	"easygo-agent/internal/handler"
	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/logger"
	mysqlplatform "easygo-agent/internal/platform/mysql"
	redisplatform "easygo-agent/internal/platform/redis"
	"easygo-agent/internal/repository"
	"easygo-agent/internal/server"
	"easygo-agent/internal/service"

	"go.uber.org/zap"
)

func Run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log, err := logger.New(logger.Config{
		Environment: cfg.App.Env,
		Level:       cfg.Log.Level,
		File:        cfg.Log.File,
		MaxSizeMB:   cfg.Log.MaxSizeMB,
		MaxBackups:  cfg.Log.MaxBackups,
		MaxAgeDays:  cfg.Log.MaxAgeDays,
		Compress:    cfg.Log.Compress,
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

	if cfg.MySQL.AutoMigrate {
		if err := db.AutoMigrate(&model.User{}); err != nil {
			return fmt.Errorf("auto migrate models: %w", err)
		}
		log.Info("database migration completed")
	}

	userRepository := repository.NewUserRepository(db)
	userService := service.NewUserService(userRepository)
	userHandler := handler.NewUserHandler(userService)
	healthHandler := handler.NewHealthHandler(map[string]handler.CheckFunc{
		"mysql": sqlDB.PingContext,
		"redis": func(ctx context.Context) error { return redisClient.Ping(ctx).Err() },
	})

	router := server.NewRouter(cfg, log, userHandler, healthHandler)
	log.Info("application initialized", zap.String("name", cfg.App.Name), zap.String("environment", cfg.App.Env))
	return server.Run(ctx, cfg.HTTP, log, router)
}
