package middleware

import (
	"fmt"
	"runtime/debug"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/platform/logger"
	"easygo-agent/internal/response"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic recovered",
					zap.String("request_id", c.GetString(requestIDKey)),
					zap.String("panic", fmt.Sprint(recovered)),
					zap.ByteString("stack", debug.Stack()),
				)
				c.Abort()
				response.Fail(c, errorcode.New(errorcode.Internal, "服务器内部错误"))
			}
		}()
		c.Next()
	}
}
