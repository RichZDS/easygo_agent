package middleware

import (
	"net/http"
	"strings"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/response"

	"github.com/gin-gonic/gin"
)

func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		_, exactMatch := allowed[origin]
		_, allowAll := allowed["*"]
		if origin != "" && (exactMatch || allowAll) {
			if allowAll {
				c.Header("Access-Control-Allow-Origin", "*")
			} else {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			}
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
			c.Header("Access-Control-Allow-Methods", strings.Join([]string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}, ", "))
			c.Header("Access-Control-Expose-Headers", RequestIDHeader)
		}

		if c.Request.Method == http.MethodOptions {
			if origin != "" && !exactMatch && !allowAll {
				c.Abort()
				response.Fail(c, errorcode.New(errorcode.Forbidden, "不允许的跨域来源"))
				return
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
