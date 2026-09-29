// Package app assembles the native agent, model gateway, tools and session UI.
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

	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/gateway"
	"easygo-agent/internal/logger"
	"easygo-agent/internal/tui"

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
	Debug      bool
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
	if opts.Mode != "cli" && opts.Mode != "gateway" && opts.Mode != "worker" {
		return errors.New("mode must be cli, gateway or worker")
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
	level := zap.InfoLevel
	if opts.Debug {
		level = zap.DebugLevel
	}
	if _, err := logger.New(logger.DefaultPath(), level); err != nil {
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

	app, err := assemble(ctx, configPath)
	if err != nil {
		return err
	}
	defer app.close()
	cfg := app.cfg
	store := app.store
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
	if err := app.buildAgent(ctx); err != nil {
		return err
	}
	if err := app.startMemory(ctx, opts); err != nil {
		return err
	}
	app.buildQueue(ctx)
	if app.tasks != nil && (opts.Mode == "gateway" || opts.Mode == "worker" || (opts.Mode == "cli" && opts.Input == "")) {
		if err = app.tasks.Start(ctx); err != nil {
			return err
		}
	}
	if opts.Mode == "worker" {
		if app.tasks == nil {
			return errors.New("worker mode requires tasks.enabled")
		}
		<-ctx.Done()
		return nil
	}
	if opts.Mode == "gateway" {
		server := &http.Server{Addr: cfg.HTTP.Address, Handler: gateway.New(store, app.queue, app.tasks), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
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
	if opts.Input != "" {
		fmt.Fprintf(os.Stderr, "user: %s · session: %s\n", opts.Username, opts.SessionID)
		record, _, submitErr := app.queue.Submit(context.Background(), opts.Username, opts.SessionID, opts.Input, "")
		if submitErr != nil {
			return submitErr
		}
		subscription, subscribeErr := app.queue.Subscribe(context.Background(), opts.Username, opts.SessionID, record.ID)
		if subscribeErr != nil {
			return subscribeErr
		}
		defer subscription.Close()
		printedText := false
		for event := range subscription.Events() {
			switch event.Kind {
			case agentruntime.EventTextDelta:
				if _, printErr := fmt.Print(event.Text); printErr != nil {
					return printErr
				}
				printedText = true
			case agentruntime.EventCompressing:
				if _, printErr := fmt.Fprintln(os.Stderr, "compressing history..."); printErr != nil {
					return printErr
				}
			case agentruntime.EventCompleted:
				if !printedText && event.Text != "" {
					if _, printErr := fmt.Print(event.Text); printErr != nil {
						return printErr
					}
				}
				fmt.Println()
				return nil
			case agentruntime.EventCanceled:
				return context.Canceled
			case agentruntime.EventFailed:
				if event.Err != nil {
					return event.Err
				}
				return errors.New("queued run failed")
			}
		}
		return errors.New("queued run subscription closed before terminal event")
	}
	ui := tui.NewQueue(app.queue, opts.Username, opts.SessionID)
	if app.tasks != nil {
		ui.WithTasks(taskLister{app.tasks.Store}, app.store.(conversation.NotificationReader))
	}
	defer func() { resultErr = errors.Join(resultErr, ui.Close()) }()
	// Subscribe to active runs before loading history. A recovered run can
	// finish while the initial history pages are being read; establishing the
	// queue observer first prevents that result from falling into the gap
	// between the two startup snapshots.
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
	lastTurnID, lines := historyLines(history)
	ui.Restore(opts.Username, opts.SessionID, lastTurnID, lines)
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
