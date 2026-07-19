package server

import (
	"net/http"

	"easygo-agent/internal/controller"
	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/handler"
	"easygo-agent/internal/middleware"
	"easygo-agent/internal/response"

	"github.com/gin-gonic/gin"
)

// NewRouter 创建 HTTP 路由
func NewRouter(health *handler.HealthHandler, userCtl *controller.UserController, sessionCtl *controller.ChatSessionController) *gin.Engine {
	router := gin.New()
	router.HandleMethodNotAllowed = true
	router.Use(
		middleware.RequestID(),
		middleware.AccessLogger(),
		middleware.Recovery(),
		middleware.CORS([]string{"http://localhost:3000", "http://localhost:5173"}),
	)

	// 健康检查
	router.GET("/healthz", health.Live)
	router.GET("/readyz", health.Ready)

	// 用户 API
	v1 := router.Group("/api/v1")
	{
		v1.POST("/users", userCtl.Create)
		v1.GET("/users/:id", userCtl.GetByID)
		v1.GET("/users", userCtl.GetByName)
		v1.PATCH("/users/:id/contact", userCtl.UpdateContact)
		v1.PATCH("/users/:id/password", userCtl.UpdatePassword)
		v1.DELETE("/users/:id", userCtl.Delete)
	}

	// 聊天会话 API
	sessions := router.Group("/api/v1")
	{
		sessions.POST("/sessions", sessionCtl.Create)
		sessions.GET("/sessions", sessionCtl.GetBySessionID)
		sessions.GET("/sessions/:id", sessionCtl.GetByID)
		sessions.GET("/users/:user_id/sessions", sessionCtl.ListByUserID)
		sessions.PATCH("/sessions/:id/title", sessionCtl.UpdateTitle)
		sessions.PATCH("/sessions/:id/status", sessionCtl.UpdateStatus)
		sessions.POST("/sessions/:id/messages", sessionCtl.RecordMessage)
		sessions.DELETE("/sessions/:id", sessionCtl.Delete)
	}

	router.NoRoute(func(c *gin.Context) {
		response.Fail(c, errorcode.New(errorcode.NotFound, "接口不存在"))
	})
	router.NoMethod(func(c *gin.Context) {
		response.JSON(c, http.StatusMethodNotAllowed, errorcode.InvalidParameter, nil, "请求方法不支持")
	})
	return router
}
