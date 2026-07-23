package server

import (
	"net/http"

	"easygo-agent/internal/auth"
	"easygo-agent/internal/controller"
	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/handler"
	"easygo-agent/internal/middleware"
	"easygo-agent/internal/response"

	"github.com/gin-gonic/gin"
)

// NewRouter 创建 HTTP 路由
func NewRouter(
	health *handler.HealthHandler,
	userCtl *controller.UserController,
	sessionCtl *controller.ChatSessionController,
	msgCtl *controller.ChatMessageController,
	authCtl *controller.AuthController,
	modelConfigCtl *controller.ModelConfigController,
	turnCtl *controller.ChatTurnController,
	issuer *auth.Issuer,
) *gin.Engine {
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

	v1 := router.Group("/api/v1")
	{
		v1.POST("/auth/login", authCtl.Login)
		// 用户 API
		v1.POST("/users", userCtl.Create)
		authorized := v1.Group("", middleware.RequireAuth(issuer))
		{
			authorized.PUT("/credentials", modelConfigCtl.PutCredential)
			authorized.POST("/model-configs", modelConfigCtl.Create)
			authorized.GET("/model-configs", modelConfigCtl.List)
			authorized.POST("/sessions/:session_id/turns", turnCtl.Create)
			authorized.POST("/chat-sessions", turnCtl.CreateSession)
			authorized.PATCH("/sessions/:session_id/model", turnCtl.SwitchModel)
			authorized.GET("/turns/:turn_id", turnCtl.Get)
			authorized.GET("/turns/:turn_id/events", turnCtl.Events)
		}
		v1.GET("/users/:id", userCtl.GetByID)
		v1.GET("/users", userCtl.GetByName)
		v1.PATCH("/users/:id/contact", userCtl.UpdateContact)
		v1.PATCH("/users/:id/password", userCtl.UpdatePassword)
		v1.DELETE("/users/:id", userCtl.Delete)

		// 聊天会话 API
		v1.POST("/sessions", sessionCtl.Create)
		v1.GET("/sessions", sessionCtl.GetBySessionID)
		v1.GET("/sessions/:id", sessionCtl.GetByID)
		v1.GET("/users/:id/sessions", sessionCtl.ListByUserID)
		v1.PATCH("/sessions/:id/title", sessionCtl.UpdateTitle)
		v1.PATCH("/sessions/:id/status", sessionCtl.UpdateStatus)
		v1.POST("/sessions/:id/messages", sessionCtl.RecordMessage)
		v1.DELETE("/sessions/:id", sessionCtl.Delete)

		// 聊天消息 API
		v1.POST("/messages", msgCtl.Create)
		v1.GET("/messages", msgCtl.GetByMessageID)
		v1.GET("/messages/:id", msgCtl.GetByID)
		v1.GET("/sessions/:id/messages", msgCtl.ListBySessionID)
		v1.PATCH("/messages/:id/status", msgCtl.UpdateStatus)
		v1.PATCH("/messages/:id/content", msgCtl.UpdateContent)
		v1.PATCH("/messages/:id/tokens", msgCtl.UpdateTokens)
		v1.PATCH("/messages/:id/error", msgCtl.SetError)
		v1.DELETE("/messages/:id", msgCtl.Delete)
	}

	router.NoRoute(func(c *gin.Context) {
		response.Fail(c, errorcode.New(errorcode.NotFound, "接口不存在"))
	})
	router.NoMethod(func(c *gin.Context) {
		response.JSON(c, http.StatusMethodNotAllowed, errorcode.InvalidParameter, nil, "请求方法不支持")
	})
	return router
}
