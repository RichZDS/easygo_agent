package app

import (
	"context"
	"fmt"
	"time"

	"easygo-agent/internal/agent/chatmodel"
	deepagent "easygo-agent/internal/agent/deepagent.go"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/logger"
	"easygo-agent/internal/tools"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// application owns resources shared by the CLI and gateway modes.
// Construction and cleanup stay together so mode code only consumes the
// assembled runtime.
type application struct {
	cfg   config.Config
	store conversation.QueueStore
	agent adk.TypedAgent[*schema.AgenticMessage]
	queue agentruntime.QueueManager
}

func assemble(ctx context.Context, configPath string) (*application, error) {
	cfg, err := config.Load(configPath, lookupEnv)
	if err != nil {
		wrappedErr := fmt.Errorf("load configuration: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "config"), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	store, err := openStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &application{cfg: cfg, store: store}, nil
}

func openStore(ctx context.Context, cfg config.Config) (conversation.QueueStore, error) {
	if cfg.Database.Driver == "memory" {
		return conversation.NewMemory(), nil
	}
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	store, err := conversation.NewPostgres(connectCtx, cfg.Database.DSN, cfg.Database.MaxConns)
	if err != nil {
		return nil, fmt.Errorf("initialize PostgreSQL: %w", err)
	}
	return store, nil
}

func (app *application) buildQueue(ctx context.Context) {
	app.queue = agentruntime.NewQueueManager(ctx, app.store, app.agent, agentruntime.QueueConfig{
		MaxPending:   app.cfg.Queue.MaxPending,
		MaxWorkers:   app.cfg.Queue.MaxWorkers,
		PollInterval: app.cfg.Queue.PollInterval,
		LeaseTTL:     app.cfg.Queue.LeaseTTL,
	})
}

func (app *application) buildAgent(ctx context.Context) error {
	allTools, err := tools.NewAgentTool().AllTools(ctx)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize tools: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "tool"), zap.Error(wrappedErr))
		return wrappedErr
	}
	mainModel, err := chatmodel.New(ctx, app.cfg.Model)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize chat model: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "model"), zap.Error(wrappedErr))
		return wrappedErr
	}
	summaryModel, err := chatmodel.New(ctx, app.cfg.SubAgent)
	if err != nil {
		return fmt.Errorf("initialize summary model: %w", err)
	}
	app.agent, err = deepagent.New(ctx, deepagent.Config{
		ChatModel:    mainModel,
		SummaryModel: summaryModel,
		Tools:        allTools,
		Agent:        app.cfg.Agent,
	})
	if err != nil {
		wrappedErr := fmt.Errorf("initialize agent: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "agent"), zap.Error(wrappedErr))
		return wrappedErr
	}
	return nil
}

func (app *application) close() {
	if app == nil {
		return
	}
	if app.queue != nil {
		_ = app.queue.Close()
	}
	if app.store != nil {
		app.store.Close()
	}
}
