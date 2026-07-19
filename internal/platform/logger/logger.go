package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
