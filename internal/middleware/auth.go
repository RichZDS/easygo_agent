package middleware

import (
	"net/http"
	"strings"

	"easygo-agent/internal/auth"
	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/response"

	"github.com/gin-gonic/gin"
)

const UserIDKey = "authenticated_user_id"

func RequireAuth(issuer *auth.Issuer) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			response.JSON(c, http.StatusUnauthorized, errorcode.Unauthorized, nil, "Bearer token required")
			c.Abort()
			return
		}
		userID, err := issuer.Verify(strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			response.JSON(c, http.StatusUnauthorized, errorcode.Unauthorized, nil, "invalid access token")
			c.Abort()
			return
		}
		c.Set(UserIDKey, userID)
		c.Next()
	}
}

func UserID(c *gin.Context) uint64 { value, _ := c.Get(UserIDKey); id, _ := value.(uint64); return id }
