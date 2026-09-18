package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	osuser "os/user"
	"strings"
	"syscall"

	"easygo-agent/internal/app"
	"easygo-agent/internal/config"
	"easygo-agent/internal/logger"

	"go.uber.org/zap"
)

// main exits with the terminal application's status code.
func main() {
	os.Exit(run())
}

// run 加载可选 .env，并使用固定配置文件启动应用。
func run() int {
	opts := app.Options{}
	defaultUser := "default"
	if user, err := osuser.Current(); err == nil {
		defaultUser = user.Username
	}
	flag.StringVar(&opts.Mode, "mode", "cli", "cli, gateway or worker")
	flag.StringVar(&opts.Username, "user", defaultUser, "conversation username")
	flag.StringVar(&opts.SessionID, "session", "", "resume a session ID (default: latest for user)")
	flag.BoolVar(&opts.NewSession, "new", false, "create a new conversation")
	flag.BoolVar(&opts.List, "list", false, "list this user's conversations")
	flag.StringVar(&opts.Input, "prompt", "", "run one prompt and exit")
	flag.BoolVar(&opts.Debug, "debug", false, "include full native messages and tool payloads in local logs")
	configPath := flag.String("config", config.DefaultPath, "YAML configuration path")
	flag.Parse()
	// 载入环境变量
	if err := loadDotEnv(".env"); err != nil {
		if _, printErr := fmt.Fprintf(os.Stderr, "application stopped: %v\n", err); printErr != nil {
			logger.Error("print application error failed", zap.Error(printErr))
		}
		return 1
	}
	// 启动应用
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// 运行应用
	if err := app.Run(ctx, *configPath, opts); err != nil {
		if _, printErr := fmt.Fprintf(os.Stderr, "application stopped: %v\n", err); printErr != nil {
			logger.Error("print application error failed", zap.Error(printErr))
		}
		return 1
	}
	return 0
}

// loadDotEnv 读取可选的 .env 文件，只填充尚未设置的环境变量。
func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		wrappedErr := fmt.Errorf("read env file: %w", err)
		logger.Error("load env file failed", zap.String("path", path), zap.Error(wrappedErr))
		return wrappedErr
	}
	// closeEnvFile 关闭已打开的 .env 文件。
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			logger.Error("close env file failed", zap.String("path", path), zap.Error(closeErr))
		}
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			wrappedErr := fmt.Errorf("set env %s: %w", key, err)
			logger.Error("load env file failed", zap.String("key", key), zap.Error(wrappedErr))
			return wrappedErr
		}
	}
	if err := scanner.Err(); err != nil {
		wrappedErr := fmt.Errorf("read env file: %w", err)
		logger.Error("load env file failed", zap.String("path", path), zap.Error(wrappedErr))
		return wrappedErr
	}
	return nil
}
