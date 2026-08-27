package logger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	// DefaultDir 是应用默认写入的日志目录。
	DefaultDir = "logs"
)

// now 返回当前时间，测试中可替换以固定日期。
var now = time.Now

// DefaultPath 返回当天日志文件路径，格式为 logs/yyyy-mm-dd.log。
func DefaultPath() string {
	return filepath.Join(DefaultDir, now().Format("2006-01-02")+".log")
}

var closeSink func()

// New 构造写入指定文件的 Zap logger，级别为 Debug，并替换进程全局 logger。
func New(path string) (*zap.Logger, error) {
	closeOpenSink()
	if strings.TrimSpace(path) == "" {
		err := errors.New("log path cannot be empty")
		Error("create logger failed", zap.Error(err))
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		wrappedErr := fmt.Errorf("create log directory: %w", err)
		Error("create logger failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		wrappedErr := fmt.Errorf("open log file: %w", err)
		Error("create logger failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(file),
		zap.NewAtomicLevelAt(zap.DebugLevel),
	)
	produced := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel))
	// syncAndClose 刷新并关闭当前日志文件。
	closeSink = func() {
		_ = produced.Sync()
		_ = file.Close()
	}
	zap.ReplaceGlobals(produced)
	return produced, nil
}

// closeOpenSink 关闭上一次 New 打开的文件 sink。
func closeOpenSink() {
	if closeSink == nil {
		return
	}
	closeSink()
	closeSink = nil
}
