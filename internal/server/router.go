package server

import (
	"net/http"

	"easygo-agent/internal/controller"
	"easygo-agent/internal/platform/auth"
	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/platform/handler"
	"easygo-agent/internal/platform/middleware"
	"easygo-agent/internal/platform/response"

	"github.com/gin-gonic/gin"
)

func NewRouter(
	health *handler.HealthHandler,
	userCtl *controller.UserController,
	authCtl *controller.AuthController,
	providerCtl *controller.ProviderController,
	chatCtl *controller.ChatController,
	skillCtl *controller.SkillController,
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

	router.GET("/healthz", health.Live)
	router.GET("/readyz", health.Ready)

	v1 := router.Group("/api/v1")
	{
		v1.POST("/auth/login", authCtl.Login)
		v1.POST("/users", userCtl.Create)
		authorized := v1.Group("", middleware.RequireAuth(issuer))
		{
			authorized.PUT("/providers", providerCtl.UpsertProvider)
			authorized.GET("/providers", providerCtl.ListProviders)
			authorized.POST("/providers/models", providerCtl.DiscoverModels)
			authorized.POST("/ai-models", providerCtl.CreateAIModel)
			authorized.GET("/ai-models", providerCtl.ListAIModels)

			authorized.POST("/sessions", chatCtl.CreateSession)
			authorized.GET("/sessions", chatCtl.ListSessions)
			authorized.GET("/sessions/:id/messages", chatCtl.ListMessages)
			authorized.PATCH("/sessions/:id/model", chatCtl.SwitchModel)
			authorized.POST("/sessions/:id/runs:stream", chatCtl.Stream)
			authorized.GET("/runs/:run_id", chatCtl.GetRun)

			authorized.POST("/skills", skillCtl.Upload)
			authorized.GET("/skills", skillCtl.List)
			authorized.DELETE("/skills/:skill_id", skillCtl.Delete)
		}
		v1.GET("/users/:id", userCtl.GetByID)
		v1.GET("/users", userCtl.GetByName)
		v1.PATCH("/users/:id/contact", userCtl.UpdateContact)
		v1.PATCH("/users/:id/password", userCtl.UpdatePassword)
		v1.DELETE("/users/:id", userCtl.Delete)
	}

	// handleUnknownRoute returns the stable not-found response envelope.
	handleUnknownRoute := func(c *gin.Context) {
		response.Fail(c, errorcode.New(errorcode.NotFound, "接口不存在"))
	}
	router.NoRoute(handleUnknownRoute)
	// handleUnsupportedMethod returns the stable method-not-allowed response envelope.
	handleUnsupportedMethod := func(c *gin.Context) {
		response.JSON(c, http.StatusMethodNotAllowed, errorcode.InvalidParameter, nil, "请求方法不支持")
	}
	router.NoMethod(handleUnsupportedMethod)
	return router
}
