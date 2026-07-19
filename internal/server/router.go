package server

import (
	"net/http"

	"easygo-agent/internal/config"
	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/handler"
	"easygo-agent/internal/middleware"
	"easygo-agent/internal/response"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func NewRouter(cfg config.Config, log *zap.Logger, users *handler.UserHandler, health *handler.HealthHandler) *gin.Engine {
	if cfg.App.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.HandleMethodNotAllowed = true
	router.Use(
		middleware.RequestID(),
		middleware.AccessLogger(log),
		middleware.Recovery(log),
		middleware.CORS(cfg.Security.AllowedOrigins),
	)

	router.GET("/healthz", health.Live)
	router.GET("/readyz", health.Ready)

	v1 := router.Group("/api/v1")
	{
		v1.POST("/users", users.Create)
		v1.GET("/users/:id", users.Get)
	}

	router.NoRoute(func(c *gin.Context) {
		response.Fail(c, errorcode.New(errorcode.NotFound, "接口不存在"))
	})
	router.NoMethod(func(c *gin.Context) {
		response.JSON(c, http.StatusMethodNotAllowed, errorcode.InvalidParameter, nil, "请求方法不支持")
	})
	return router
}
