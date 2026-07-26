package middleware

import (
	"crypto/rand"
	"encoding/hex"

	"easygo-agent/internal/platform/requestid"
	"easygo-agent/internal/platform/response"

	"github.com/gin-gonic/gin"
)

const RequestIDHeader = "X-Request-ID"

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" {
			id = newRequestID()
		}

		c.Set(response.RequestIDKey, id)
		c.Header(RequestIDHeader, id)
		c.Request = c.Request.WithContext(requestid.With(c.Request.Context(), id))
		requestid.Bind(id)
		defer requestid.Unbind()
		c.Next()
	}
}

func newRequestID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(value)
}
