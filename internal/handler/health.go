package handler

import (
	"context"
	"net/http"
	"time"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/response"

	"github.com/gin-gonic/gin"
)

// CheckFunc 检查函数类型
type CheckFunc func(context.Context) error

type HealthHandler struct {
	checks map[string]CheckFunc
}

// NewHealthHandler 创建健康检查处理器
func NewHealthHandler(checks map[string]CheckFunc) *HealthHandler {
	return &HealthHandler{checks: checks}
}

// Live 检查服务是否存活
func (h *HealthHandler) Live(c *gin.Context) {
	response.Success(c, gin.H{"status": "ok"})
}

// Ready 检查服务是否准备好
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	components := make(map[string]string, len(h.checks))
	healthy := true
	for name, check := range h.checks {
		if err := check(ctx); err != nil {
			components[name] = "down"
			healthy = false
			continue
		}
		components[name] = "up"
	}

	data := gin.H{"status": "ok", "components": components}
	if !healthy {
		data["status"] = "degraded"
		response.JSON(c, http.StatusServiceUnavailable, errorcode.Internal, data, "依赖服务不可用")
		return
	}
	response.Success(c, data)
}
