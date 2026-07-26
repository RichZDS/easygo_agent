package server

import (
	"net/http"

	"easygo-agent/internal/auth"
	"easygo-agent/internal/controller"
	"easygo-agent/internal/handler"
	"easygo-agent/internal/middleware"
	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/response"

	"github.com/gin-gonic/gin"
)

// NewRouter 创建 HTTP 路由
func NewRouter(
	health *handler.HealthHandler,
	userCtl *controller.UserController,
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
			authorized.POST("/sessions/:id/turns:stream", turnCtl.Stream)
			authorized.POST("/sessions", turnCtl.CreateSession)
			authorized.GET("/sessions", turnCtl.ListSessions)
			authorized.GET("/sessions/:session_id/messages", turnCtl.ListMessages)
			authorized.PATCH("/sessions/:id/model", turnCtl.SwitchModel)
			authorized.GET("/turns/:turn_id", turnCtl.Get)
		}
		v1.GET("/users/:id", userCtl.GetByID)
		v1.GET("/users", userCtl.GetByName)
		v1.PATCH("/users/:id/contact", userCtl.UpdateContact)
		v1.PATCH("/users/:id/password", userCtl.UpdatePassword)
		v1.DELETE("/users/:id", userCtl.Delete)

	}

	router.NoRoute(func(c *gin.Context) {
		response.Fail(c, errorcode.New(errorcode.NotFound, "接口不存在"))
	})
	router.NoMethod(func(c *gin.Context) {
		response.JSON(c, http.StatusMethodNotAllowed, errorcode.InvalidParameter, nil, "请求方法不支持")
	})
	return router
}
