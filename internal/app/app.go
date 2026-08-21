// Package app assembles and runs the Eino TUI template.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"easygo-agent/internal/chatmodel"
	"easygo-agent/internal/config"
	"easygo-agent/internal/gateway"
	"easygo-agent/internal/observability"
	"easygo-agent/internal/tools"
	"easygo-agent/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/components/tool"
	"go.uber.org/zap"
)

type programRunner func(*tea.Program, *zap.Logger) (tea.Model, error)

// Run loads configuration and runs the terminal application.
func Run(ctx context.Context, configPath string) error {
	err := run(ctx, configPath, os.LookupEnv, runTeaProgram)
	if err != nil {
		zap.L().Error("application failed", zap.String("stage", "app_run"), zap.Error(err))
		return err
	}
	return nil
}

// run assembles dependencies with injectable environment and terminal execution for tests.
func run(
	ctx context.Context,
	configPath string,
	lookupEnv func(string) (string, bool),
	runProgram programRunner,
) (resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	logger, err := observability.NewLogger()
	if err != nil {
		wrappedErr := fmt.Errorf("initialize logger: %w", err)
		zap.NewNop().Error("assemble application failed", zap.String("stage", "logger"), zap.Error(wrappedErr))
		return wrappedErr
	}
	var tracing *observability.Tracing
	// cleanup flushes tracing and logging after every assembly outcome.
	defer func() {
		if tracing != nil {
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			shutdownErr := tracing.Shutdown(shutdownContext)
			cancel()
			if shutdownErr != nil {
				logger.Error("assemble application cleanup failed", zap.String("stage", "tracing"), zap.Error(shutdownErr))
				resultErr = errors.Join(resultErr, shutdownErr)
			}
		}
		if syncErr := syncLogger(logger); syncErr != nil {
			resultErr = errors.Join(resultErr, syncErr)
		}
	}()

	cfg, err := config.Load(configPath, lookupEnv, logger)
	if err != nil {
		wrappedErr := fmt.Errorf("load configuration: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "config"), zap.Error(wrappedErr))
		return wrappedErr
	}
	tracing, err = observability.NewTracing(ctx, cfg.Tracing, logger)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize tracing: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "tracing"), zap.Error(wrappedErr))
		return wrappedErr
	}
	calculator, err := tools.NewCalculator(logger)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize Calculator Tool: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "tool"), zap.Error(wrappedErr))
		return wrappedErr
	}
	model, err := chatmodel.New(ctx, cfg.Model, logger)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize chat model: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "model"), zap.Error(wrappedErr))
		return wrappedErr
	}
	agentGateway, err := gateway.New(
		ctx,
		gateway.Config{MaxSteps: cfg.Agent.MaxSteps},
		model,
		[]tool.BaseTool{calculator},
		tracing.Tracer,
		tracing.Handler,
		logger,
	)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize Gateway: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "gateway"), zap.Error(wrappedErr))
		return wrappedErr
	}
	program := tea.NewProgram(
		tui.New(agentGateway, cfg.Agent.SystemPrompt, logger),
		tea.WithAltScreen(),
		tea.WithContext(ctx),
	)
	logger.Info("application initialized", zap.String("mode", "tui"), zap.String("model", cfg.Model.Name))
	if _, err := runProgram(program, logger); err != nil {
		wrappedErr := fmt.Errorf("run terminal UI: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "tui"), zap.Error(wrappedErr))
		return wrappedErr
	}
	return nil
}

// runTeaProgram executes one Bubble Tea program.
func runTeaProgram(program *tea.Program, logger *zap.Logger) (tea.Model, error) {
	model, err := program.Run()
	if err != nil {
		wrappedErr := fmt.Errorf("run Bubble Tea program: %w", err)
		logger.Error("run terminal program failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return model, nil
}

// syncLogger flushes Zap while ignoring terminal-specific invalid sync errors.
func syncLogger(logger *zap.Logger) error {
	err := logger.Sync()
	if err == nil || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.EBADF) {
		return nil
	}
	wrappedErr := fmt.Errorf("sync logger: %w", err)
	logger.Error("sync logger failed", zap.Error(wrappedErr))
	return wrappedErr
}
