package controller

import (
	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/platform/response"
	"easygo-agent/internal/service/account"

	"github.com/gin-gonic/gin"
)

type AuthController struct{ service *account.Service }

func NewAuthController(service *account.Service) *AuthController { return &AuthController{service} }
func (ctl *AuthController) Login(c *gin.Context) {
	var req struct {
		Name     string `json:"name" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid login request"))
		return
	}
	token, err := ctl.service.Login(c.Request.Context(), req.Name, req.Password)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, gin.H{"access_token": token, "token_type": "Bearer"})
}
