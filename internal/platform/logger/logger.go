package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

type Config struct {
	Environment string
	Level       string
	File        string
	MaxSizeMB   int
	MaxBackups  int
	MaxAgeDays  int
	Compress    bool
}

var (
	mu     sync.RWMutex
	global *zap.Logger = zap.NewNop()
	// skip1 供包级 Info/Error 等使用，跳过一层包装后正确显示调用方文件行号
	skip1 *zap.Logger = zap.NewNop()
)

// Init 初始化全局日志，整个程序共用一份。启动时调用一次即可。
func Init(cfg Config) error {
	log, err := New(cfg)
	if err != nil {
		return err
	}
	mu.Lock()
	global = log
	skip1 = log.WithOptions(zap.AddCallerSkip(1))
	mu.Unlock()
	zap.ReplaceGlobals(log)
	return nil
}

// L 返回全局 *zap.Logger，需要链式调用（如 With）时使用。
func L() *zap.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return global
}

// Sync 刷新全局日志缓冲，进程退出前调用。
func Sync() error {
	mu.RLock()
	log := global
	mu.RUnlock()
	return log.Sync()
}

func Debug(msg string, fields ...zap.Field) {
	mu.RLock()
	log := skip1
	mu.RUnlock()
	log.Debug(msg, fields...)
}

func Info(msg string, fields ...zap.Field) {
	mu.RLock()
	log := skip1
	mu.RUnlock()
	log.Info(msg, fields...)
}

func Warn(msg string, fields ...zap.Field) {
	mu.RLock()
	log := skip1
	mu.RUnlock()
	log.Warn(msg, fields...)
}

func Error(msg string, fields ...zap.Field) {
	mu.RLock()
	log := skip1
	mu.RUnlock()
	log.Error(msg, fields...)
}

func Fatal(msg string, fields ...zap.Field) {
	mu.RLock()
	log := skip1
	mu.RUnlock()
	log.Fatal(msg, fields...)
}

func Panic(msg string, fields ...zap.Field) {
	mu.RLock()
	log := skip1
	mu.RUnlock()
	log.Panic(msg, fields...)
}

// With 基于全局 logger 派生带固定字段的子 logger。
func With(fields ...zap.Field) *zap.Logger {
	return L().With(fields...)
}

func New(cfg Config) (*zap.Logger, error) {
	level := zapcore.InfoLevel
	if err := level.Set(strings.ToLower(cfg.Level)); err != nil {
		return nil, fmt.Errorf("parse log level: %w", err)
	}

	baseEncoder := zap.NewProductionEncoderConfig()
	baseEncoder.TimeKey = "timestamp"
	baseEncoder.EncodeTime = zapcore.ISO8601TimeEncoder
	baseEncoder.EncodeDuration = zapcore.StringDurationEncoder

	consoleEncoder := baseEncoder
	consoleEncoder.EncodeLevel = zapcore.CapitalColorLevelEncoder
	if cfg.Environment != "development" {
		consoleEncoder.EncodeLevel = zapcore.LowercaseLevelEncoder
	}

	cores := []zapcore.Core{
		zapcore.NewCore(zapcore.NewConsoleEncoder(consoleEncoder), zapcore.Lock(os.Stdout), level),
	}

	if cfg.File != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.File), 0o755); err != nil {
			return nil, fmt.Errorf("create log directory: %w", err)
		}
		fileWriter := zapcore.AddSync(&lumberjack.Logger{
			Filename:   cfg.File,
			MaxSize:    cfg.MaxSizeMB,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAgeDays,
			Compress:   cfg.Compress,
		})
		cores = append(cores, zapcore.NewCore(zapcore.NewJSONEncoder(baseEncoder), fileWriter, level))
	}

	return zap.New(zapcore.NewTee(cores...), zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel)), nil
}
