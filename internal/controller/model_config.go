package controller

import (
	"easygo-agent/internal/middleware"
	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/response"
	"easygo-agent/internal/service/modelconfig"

	"github.com/gin-gonic/gin"
)

type ModelConfigController struct{ service *modelconfig.Service }

func NewModelConfigController(s *modelconfig.Service) *ModelConfigController {
	return &ModelConfigController{s}
}
func (ctl *ModelConfigController) PutCredential(c *gin.Context) {
	var req struct {
		Provider string `json:"provider" binding:"required"`
		APIKey   string `json:"api_key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid credential"))
		return
	}
	v, err := ctl.service.UpsertCredential(c.Request.Context(), middleware.UserID(c), req.Provider, req.APIKey)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, gin.H{"id": v.ID, "provider_name": v.ProviderName, "status": v.Status})
}
func (ctl *ModelConfigController) Create(c *gin.Context) {
	var req struct {
		CredentialID     uint64         `json:"credential_id" binding:"required"`
		Provider         string         `json:"provider" binding:"required"`
		ModelName        string         `json:"model_name" binding:"required"`
		BaseURL          *string        `json:"base_url"`
		MaxContextTokens uint32         `json:"max_context_tokens" binding:"required"`
		MaxOutputTokens  uint32         `json:"max_output_tokens" binding:"required"`
		Settings         *model.JSONMap `json:"settings"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid model config"))
		return
	}
	v, err := ctl.service.Create(c.Request.Context(), middleware.UserID(c), req.CredentialID, req.Provider, req.ModelName, req.BaseURL, req.MaxContextTokens, req.MaxOutputTokens, req.Settings)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, v)
}
func (ctl *ModelConfigController) List(c *gin.Context) {
	v, err := ctl.service.List(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, v)
}
