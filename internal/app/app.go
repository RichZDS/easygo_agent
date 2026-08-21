// Package app 组装并运行 Eino TUI 模板。
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
	"go.uber.org/zap"
)

type programRunner func(*tea.Program, *zap.Logger) (tea.Model, error)

// Run 加载配置并运行终端应用。
func Run(ctx context.Context, configPath string) error {
	// 运行应用
	err := run(ctx, configPath, os.LookupEnv, runTeaProgram)
	if err != nil {
		zap.L().Error("application failed", zap.String("stage", "app_run"), zap.Error(err))
		return err
	}
	return nil
}

// run 组装依赖；测试可注入环境查找与终端执行。
func run(
	ctx context.Context,
	configPath string,
	lookupEnv func(string) (string, bool),
	runProgram programRunner, // Bubble Tea
) (resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// 初始化日志
	logger, err := observability.NewLogger()
	if err != nil {
		wrappedErr := fmt.Errorf("initialize logger: %w", err)
		zap.NewNop().Error("assemble application failed", zap.String("stage", "logger"), zap.Error(wrappedErr))
		return wrappedErr
	}
	var tracing *observability.Tracing
	// cleanup 在每次组装结束后刷新 tracing 与日志。
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

	// 加载 YAML 配置，并把 {ENV} 形式的 apikey 解析为环境变量。
	cfg, err := config.Load(configPath, lookupEnv, logger)
	if err != nil {
		wrappedErr := fmt.Errorf("load configuration: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "config"), zap.Error(wrappedErr))
		return wrappedErr
	}

	// 按配置初始化 OpenTelemetry tracing；失败时后续组装全部中止。
	tracing, err = observability.NewTracing(ctx, cfg.Tracing, logger)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize tracing: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "tracing"), zap.Error(wrappedErr))
		return wrappedErr
	}
	// 集中构造全部内置 Tool，供 ReAct Agent 调用。
	allTools, err := tools.NewAgentTool(logger).AllTools(ctx)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize tools: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "tool"), zap.Error(wrappedErr))
		return wrappedErr
	}
	// 创建单个 OpenAI 兼容 Chat Model，作为 Agent 的推理后端。
	model, err := chatmodel.New(ctx, cfg.Model, logger)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize chat model: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "model"), zap.Error(wrappedErr))
		return wrappedErr
	}
	// 组装 Gateway：把模型、Tool、tracing 接到 Eino ReAct，对 TUI 隐藏流式细节。
	agentGateway, err := gateway.New(
		ctx,
		gateway.Config{MaxSteps: cfg.Agent.MaxSteps},
		model,
		allTools,
		tracing.Tracer,
		tracing.Handler,
		logger,
	)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize Gateway: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "gateway"), zap.Error(wrappedErr))
		return wrappedErr
	}
	// 启动 Bubble Tea TUI，占用备用屏幕；退出后由 ctx 取消。
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

// runTeaProgram 执行一个 Bubble Tea 程序。
func runTeaProgram(program *tea.Program, logger *zap.Logger) (tea.Model, error) {
	model, err := program.Run()
	if err != nil {
		wrappedErr := fmt.Errorf("run Bubble Tea program: %w", err)
		logger.Error("run terminal program failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return model, nil
}

// syncLogger 刷新 Zap，并忽略终端场景下的无效 Sync 错误。
func syncLogger(logger *zap.Logger) error {
	err := logger.Sync()
	if err == nil || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.EBADF) {
		return nil
	}
	wrappedErr := fmt.Errorf("sync logger: %w", err)
	logger.Error("sync logger failed", zap.Error(wrappedErr))
	return wrappedErr
}
