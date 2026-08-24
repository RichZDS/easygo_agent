// Package app 组装并运行 Eino TUI 模板。
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"easygo-agent/internal/agent/chatmodel"
	deepagent "easygo-agent/internal/agent/deepagent.go"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/logger"
	"easygo-agent/internal/observability"
	"easygo-agent/internal/prompt"
	"easygo-agent/internal/tools"
	"easygo-agent/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
)

type programRunner func(*tea.Program) (tea.Model, error)

var (
	lookupEnv                = os.LookupEnv
	runProgram programRunner = runTeaProgram
)

// Run 加载配置并运行终端应用。
func Run(ctx context.Context, configPath string) (resultErr error) {
	// 运行应用
	if ctx == nil {
		ctx = context.Background()
	}
	// 初始化日志
	if _, err := observability.NewLogger(observability.DefaultPath); err != nil {
		wrappedErr := fmt.Errorf("initialize logger: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "logger"), zap.Error(wrappedErr))
		return wrappedErr
	}
	// cleanup 在每次组装结束后刷新日志。
	defer func() {
		if syncErr := syncLogger(); syncErr != nil {
			resultErr = errors.Join(resultErr, syncErr)
		}
	}()

	// 加载 YAML 配置，并把 {ENV} 形式的 apikey 解析为环境变量值。
	cfg, err := config.Load(configPath, lookupEnv)
	if err != nil {
		wrappedErr := fmt.Errorf("load configuration: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "config"), zap.Error(wrappedErr))
		return wrappedErr
	}

	// 集中构造全部内置 Tool，供 ReAct Agent 调用。
	allTools, err := tools.NewAgentTool().AllTools(ctx)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize tools: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "tool"), zap.Error(wrappedErr))
		return wrappedErr
	}
	// 创建单个 OpenAI 兼容 Chat Model，作为 Agent 的推理后端。
	model, err := chatmodel.New(ctx, cfg.Model)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize chat model: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "model"), zap.Error(wrappedErr))
		return wrappedErr
	}
	// 构造 Eino Deep Agent，并由独立会话模块管理运行、流和对话历史。
	agent, err := deepagent.New(ctx, model, allTools, cfg.Agent)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize agent: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "agent"), zap.Error(wrappedErr))
		return wrappedErr
	}
	conversation := agentruntime.New(ctx, agent, prompt.SystemPrompt)
	// 启动 Bubble Tea TUI，占用备用屏幕；退出后由 ctx 取消。
	program := tea.NewProgram(
		tui.New(conversation),
		tea.WithAltScreen(),
		tea.WithContext(ctx),
	)
	logger.Info("application initialized", zap.String("mode", "tui"), zap.String("model", cfg.Model.Name))
	if _, err := runProgram(program); err != nil {
		wrappedErr := fmt.Errorf("run terminal UI: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "tui"), zap.Error(wrappedErr))
		return wrappedErr
	}
	return nil
}

// runTeaProgram 执行一个 Bubble Tea 程序。
func runTeaProgram(program *tea.Program) (tea.Model, error) {
	model, err := program.Run()
	if err != nil {
		wrappedErr := fmt.Errorf("run Bubble Tea program: %w", err)
		logger.Error("run terminal program failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return model, nil
}

// syncLogger 刷新 Zap，并忽略终端场景下的无效 Sync 错误。
func syncLogger() error {
	err := logger.Sync()
	if isIgnorableSyncError(err) {
		return nil
	}
	wrappedErr := fmt.Errorf("sync logger: %w", err)
	logger.Error("sync logger failed", zap.Error(wrappedErr))
	return wrappedErr
}

// isIgnorableSyncError 判断 Sync 失败是否来自无法 flush 的控制台句柄。
func isIgnorableSyncError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.EBADF) {
		return true
	}
	// Windows 控制台 stderr/stdout 不能 flush，Zap Sync 会返回 ERROR_INVALID_HANDLE (6)。
	return errors.Is(err, syscall.Errno(6))
}
