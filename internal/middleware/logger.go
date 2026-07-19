package middleware

import (
	"time"

	"easygo-agent/internal/platform/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func AccessLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		path := c.Request.URL.Path

		c.Next()

		fields := []zap.Field{
			zap.String("request_id", c.GetString(requestIDKey)),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(started)),
			zap.String("client_ip", c.ClientIP()),
			zap.String("user_agent", c.Request.UserAgent()),
			zap.Int("response_size", c.Writer.Size()),
		}
		if len(c.Errors) > 0 {
			fields = append(fields, zap.String("errors", c.Errors.String()))
		}

		switch status := c.Writer.Status(); {
		case status >= 500:
			logger.Error("http request", fields...)
		case status >= 400:
			logger.Warn("http request", fields...)
		default:
			logger.Info("http request", fields...)
		}
	}
}
