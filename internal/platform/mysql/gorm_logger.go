package mysql

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type gormLogger struct {
	log           *zap.Logger
	level         gormlogger.LogLevel
	slowThreshold time.Duration
}

func newGORMLogger(log *zap.Logger) gormlogger.Interface {
	return &gormLogger{log: log, level: gormlogger.Warn, slowThreshold: 200 * time.Millisecond}
}

func (l *gormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	clone := *l
	clone.level = level
	return &clone
}

func (l *gormLogger) Info(ctx context.Context, message string, args ...any) {
	if l.level >= gormlogger.Info {
		l.log.Info("gorm: "+message, zap.Any("args", args))
	}
}

func (l *gormLogger) Warn(ctx context.Context, message string, args ...any) {
	if l.level >= gormlogger.Warn {
		l.log.Warn("gorm: "+message, zap.Any("args", args))
	}
}

func (l *gormLogger) Error(ctx context.Context, message string, args ...any) {
	if l.level >= gormlogger.Error {
		l.log.Error("gorm: "+message, zap.Any("args", args))
	}
}

func (l *gormLogger) Trace(ctx context.Context, begin time.Time, fn func() (string, int64), err error) {
	if l.level == gormlogger.Silent {
		return
	}
	elapsed := time.Since(begin)
	sql, rows := fn()
	fields := []zap.Field{
		zap.Duration("elapsed", elapsed),
		zap.Int64("rows", rows),
		zap.String("sql", sql),
	}

	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound) && l.level >= gormlogger.Error:
		l.log.Error("gorm query failed", append(fields, zap.Error(err))...)
	case elapsed > l.slowThreshold && l.level >= gormlogger.Warn:
		l.log.Warn("gorm slow query", fields...)
	case l.level >= gormlogger.Info:
		l.log.Debug("gorm query", fields...)
	}
}
