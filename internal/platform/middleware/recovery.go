package middleware

import (
	"fmt"
	"runtime/debug"

	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/platform/logger"
	"easygo-agent/internal/platform/response"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic recovered",
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
