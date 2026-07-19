package server

import (
	"net/http"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/handler"
	"easygo-agent/internal/middleware"
	"easygo-agent/internal/response"

	"github.com/gin-gonic/gin"
)

func NewRouter(health *handler.HealthHandler) *gin.Engine {
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

	router.NoRoute(func(c *gin.Context) {
		response.Fail(c, errorcode.New(errorcode.NotFound, "接口不存在"))
	})
	router.NoMethod(func(c *gin.Context) {
		response.JSON(c, http.StatusMethodNotAllowed, errorcode.InvalidParameter, nil, "请求方法不支持")
	})
	return router
}
