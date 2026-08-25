// Package logger 封装进程级全局 Zap logger，供各包直接调用。
package logger

import "go.uber.org/zap"

// Error 使用全局 Zap logger 记录错误。
func Error(msg string, fields ...zap.Field) {
	zap.L().Error(msg, fields...)
}

// Info 使用全局 Zap logger 记录信息。
func Info(msg string, fields ...zap.Field) {
	zap.L().Info(msg, fields...)
}

// Debug 使用全局 Zap logger 记录调试信息。
func Debug(msg string, fields ...zap.Field) {
	zap.L().Debug(msg, fields...)
}

// Sync 刷新全局 Zap logger 缓冲。
func Sync() error {
	return zap.L().Sync()
}
