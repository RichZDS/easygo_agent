package logger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"easygo-agent/internal/platform/requestid"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

const traceIDField = "trace_id"

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
	base().Debug(msg, withTraceID(nil, fields)...)
}

func Info(msg string, fields ...zap.Field) {
	base().Info(msg, withTraceID(nil, fields)...)
}

func Warn(msg string, fields ...zap.Field) {
	base().Warn(msg, withTraceID(nil, fields)...)
}

func Error(msg string, fields ...zap.Field) {
	base().Error(msg, withTraceID(nil, fields)...)
}

func Fatal(msg string, fields ...zap.Field) {
	base().Fatal(msg, withTraceID(nil, fields)...)
}

func Panic(msg string, fields ...zap.Field) {
	base().Panic(msg, withTraceID(nil, fields)...)
}

// With 基于全局 logger 派生带固定字段的子 logger。
func With(fields ...zap.Field) *zap.Logger {
	return L().With(fields...)
}

// FromContext 返回带 trace_id 字段的 logger；无 id 时退回全局 logger。
func FromContext(ctx context.Context) *zap.Logger {
	log := L()
	if id := resolveTraceID(ctx); id != "" {
		return log.With(zap.String(traceIDField, id))
	}
	return log
}

func DebugContext(ctx context.Context, msg string, fields ...zap.Field) {
	base().Debug(msg, withTraceID(ctx, fields)...)
}

func InfoContext(ctx context.Context, msg string, fields ...zap.Field) {
	base().Info(msg, withTraceID(ctx, fields)...)
}

func WarnContext(ctx context.Context, msg string, fields ...zap.Field) {
	base().Warn(msg, withTraceID(ctx, fields)...)
}

func ErrorContext(ctx context.Context, msg string, fields ...zap.Field) {
	base().Error(msg, withTraceID(ctx, fields)...)
}

func base() *zap.Logger {
	mu.RLock()
	log := skip1
	mu.RUnlock()
	return log
}

func resolveTraceID(ctx context.Context) string {
	if id := requestid.From(ctx); id != "" {
		return id
	}
	return requestid.Current()
}

func withTraceID(ctx context.Context, fields []zap.Field) []zap.Field {
	if hasField(fields, traceIDField) {
		return fields
	}
	id := resolveTraceID(ctx)
	if id == "" {
		return fields
	}
	out := make([]zap.Field, 0, len(fields)+1)
	out = append(out, zap.String(traceIDField, id))
	return append(out, fields...)
}

func hasField(fields []zap.Field, key string) bool {
	for _, f := range fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

func New(cfg Config) (*zap.Logger, error) {
	level := zapcore.InfoLevel
	if err := level.Set(strings.ToLower(cfg.Level)); err != nil {
		return nil, fmt.Errorf("parse log level: %w", err)
	}

	fileEncoderCfg := zap.NewProductionEncoderConfig()
	fileEncoderCfg.TimeKey = "timestamp"
	fileEncoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	fileEncoderCfg.EncodeDuration = zapcore.StringDurationEncoder

	color := cfg.Environment == "development"
	cores := []zapcore.Core{
		zapcore.NewCore(newPrettyConsoleEncoder(color), zapcore.Lock(os.Stdout), level),
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
		cores = append(cores, zapcore.NewCore(zapcore.NewJSONEncoder(fileEncoderCfg), fileWriter, level))
	}

	return zap.New(zapcore.NewTee(cores...), zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel)), nil
}
