// Package observability 为模板构造日志。
package observability

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"easygo-agent/internal/logger"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	// DefaultPath 是应用默认写入的日志文件。
	DefaultPath = "logs/app.log"
)

var closeSink func()

// NewLogger 构造写入指定文件的生产级 Zap logger。
func NewLogger(path string) (*zap.Logger, error) {
	closeOpenSink()
	if strings.TrimSpace(path) == "" {
		err := errors.New("log path cannot be empty")
		logger.Error("create logger failed", zap.Error(err))
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		wrappedErr := fmt.Errorf("create log directory: %w", err)
		logger.Error("create logger failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		wrappedErr := fmt.Errorf("open log file: %w", err)
		logger.Error("create logger failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(file),
		zap.NewAtomicLevelAt(zap.InfoLevel),
	)
	produced := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel))
	closeSink = func() {
		_ = produced.Sync()
		_ = file.Close()
	}
	zap.ReplaceGlobals(produced)
	return produced, nil
}

func closeOpenSink() {
	if closeSink == nil {
		return
	}
	closeSink()
	closeSink = nil
}
