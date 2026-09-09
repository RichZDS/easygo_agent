// Package app 组装并运行 Eino TUI 模板。
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"easygo-agent/internal/agent/chatmodel"
	deepagent "easygo-agent/internal/agent/deepagent.go"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/gateway"
	"easygo-agent/internal/logger"
	"easygo-agent/internal/tools"
	"easygo-agent/internal/tui"
	"easygo-agent/internal/usermemory"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
)

type programRunner func(*tea.Program) (tea.Model, error)

type Options struct {
	Mode       string
	Username   string
	SessionID  string
	NewSession bool
	List       bool
	Input      string
}

var (
	lookupEnv                = os.LookupEnv
	runProgram programRunner = runTeaProgram
)

// Run 加载配置并运行终端应用。
func Run(ctx context.Context, configPath string, options ...Options) (resultErr error) {
	opts := Options{Mode: "cli", Username: "default"}
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.Mode != "cli" && opts.Mode != "gateway" {
		return errors.New("mode must be cli or gateway")
	}
	if opts.Mode == "cli" {
		if err := conversation.ValidateUser(opts.Username); err != nil {
			return err
		}
		if opts.NewSession && opts.SessionID != "" {
			return errors.New("-new and -session are mutually exclusive")
		}
	}
	// 运行应用
	if ctx == nil {
		ctx = context.Background()
	}
	// 初始化日志
	if _, err := logger.New(logger.DefaultPath()); err != nil {
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
	var store conversation.Store
	if cfg.Database.Driver == "memory" {
		store = conversation.NewMemory()
	} else {
		connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		store, err = conversation.NewPostgres(connectCtx, cfg.Database.DSN, cfg.Database.MaxConns)
		cancel()
		if err != nil {
			return fmt.Errorf("initialize PostgreSQL: %w", err)
		}
	}
	defer store.Close()
	var memoryStore conversation.MemoryStore
	if cfg.Memory.Enabled {
		if cfg.Database.Driver == "memory" {
			var ok bool
			memoryStore, ok = store.(conversation.MemoryStore)
			if !ok {
				return errors.New("in-memory conversation store does not support long-term memory")
			}
		} else {
			memoryStore, err = conversation.NewMemoryPostgres(ctx, cfg.Memory.DSN, cfg.Memory.MaxConns)
			if err != nil {
				return fmt.Errorf("initialize dedicated memory PostgreSQL: %w", err)
			}
			defer memoryStore.(*conversation.MemoryPostgres).Close()
		}
	}
	if opts.Mode == "cli" && opts.List {
		for offset := 0; ; offset += 100 {
			items, listErr := store.List(ctx, opts.Username, 100, offset)
			if listErr != nil {
				return listErr
			}
			for _, s := range items {
				fmt.Printf("%s\t%s\t%s\n", s.ID, s.Username, s.UpdatedAt.Format(time.RFC3339))
			}
			if len(items) < 100 {
				return nil
			}
		}
	}
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
	summaryModel, err := chatmodel.New(ctx, cfg.SubAgent)
	if err != nil {
		return fmt.Errorf("initialize summary model: %w", err)
	}
	agent, err := deepagent.New(ctx, model, allTools, cfg.Agent, summaryModel)
	if err != nil {
		wrappedErr := fmt.Errorf("initialize agent: %w", err)
		logger.Error("assemble application failed", zap.String("stage", "agent"), zap.Error(wrappedErr))
		return wrappedErr
	}
	if memoryStore != nil {
		memoryAgent, memoryErr := usermemory.NewModelAgent(ctx, summaryModel)
		if memoryErr != nil {
			return memoryErr
		}
		writer, memoryErr := usermemory.NewFileProfileWriter(cfg.Memory.StorageRoot)
		if memoryErr != nil {
			return memoryErr
		}
		memoryService, memoryErr := usermemory.NewService(memoryStore, store, memoryAgent, writer, cfg.Memory)
		if memoryErr != nil {
			return fmt.Errorf("initialize user-memory service: %w", memoryErr)
		}
		if opts.Mode == "gateway" || (opts.Mode == "cli" && opts.Input == "" && !opts.List) {
			if memoryErr = memoryService.StartScheduler(ctx); memoryErr != nil {
				return fmt.Errorf("start user-memory scheduler: %w", memoryErr)
			}
		}
	}
	if opts.Mode == "gateway" {
		server := &http.Server{Addr: cfg.HTTP.Address, Handler: gateway.New(store, agent, memoryStore), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
		return serve(ctx, server)
	}
	if opts.SessionID == "" && !opts.NewSession {
		sessions, listErr := store.List(ctx, opts.Username, 1, 0)
		if listErr != nil {
			return listErr
		}
		if len(sessions) > 0 {
			opts.SessionID = sessions[0].ID
		}
	}
	if opts.SessionID == "" {
		session, createErr := store.Create(ctx, opts.Username)
		if createErr != nil {
			return createErr
		}
		opts.SessionID = session.ID
	}
	sessionRuntime := agentruntime.NewStored(ctx, agent, store, opts.Username, opts.SessionID, memoryStore)
	if opts.Input != "" {
		fmt.Fprintf(os.Stderr, "user: %s · session: %s\n", opts.Username, opts.SessionID)
		run, startErr := sessionRuntime.Start(opts.Input)
		if startErr != nil {
			return startErr
		}
		defer run.Close()
		for {
			event := run.Next()
			switch event.Kind {
			case agentruntime.EventTextDelta:
				fmt.Print(event.Text)
			case agentruntime.EventCompressing:
				fmt.Fprintln(os.Stderr, "compressing history...")
			case agentruntime.EventCompleted:
				fmt.Println()
				return nil
			case agentruntime.EventCanceled:
				return context.Canceled
			case agentruntime.EventFailed:
				return event.Err
			}
		}
	}
	var history []conversation.Turn
	var after int64
	for {
		turns, historyErr := store.History(ctx, opts.Username, opts.SessionID, after, 100)
		if historyErr != nil {
			return historyErr
		}
		history = append(history, turns...)
		if len(turns) < 100 {
			break
		}
		after = turns[len(turns)-1].ID
	}
	ui := tui.New(sessionRuntime)
	defer func() { resultErr = errors.Join(resultErr, ui.Close()) }()
	ui.Restore(opts.Username, opts.SessionID, history)
	// 启动 Bubble Tea TUI，占用备用屏幕；退出后由 ctx 取消。
	program := tea.NewProgram(
		ui,
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

func serve(ctx context.Context, server *http.Server) error {
	server.BaseContext = func(_ net.Listener) context.Context { return ctx }
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	logger.Info("HTTP gateway listening", zap.String("address", server.Addr))
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			_ = server.Close()
		}
		<-done
		return err
	}
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
