package controller

import (
	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/platform/middleware"
	"easygo-agent/internal/platform/response"
	"easygo-agent/internal/service/providerconfig"

	"github.com/gin-gonic/gin"
)

type ProviderController struct {
	svc *providerconfig.Service
}

func NewProviderController(svc *providerconfig.Service) *ProviderController {
	return &ProviderController{svc: svc}
}

func (ctl *ProviderController) UpsertProvider(c *gin.Context) {
	var req struct {
		Name      string  `json:"name"`
		Type      string  `json:"type" binding:"required"`
		APIKey    string  `json:"api_key" binding:"required"`
		BaseURL   *string `json:"base_url"`
		TestModel string  `json:"test_model"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid provider request"))
		return
	}
	row, err := ctl.svc.UpsertProvider(c.Request.Context(), middleware.UserID(c), providerconfig.UpsertProviderInput{
		Name:      req.Name,
		Type:      req.Type,
		APIKey:    req.APIKey,
		BaseURL:   req.BaseURL,
		TestModel: req.TestModel,
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, gin.H{
		"id":         row.ID,
		"name":       row.Name,
		"type":       row.Type,
		"base_url":   row.BaseURL,
		"test_model": row.TestModel,
		"status":     row.Status,
		"created_at": row.CreatedAt,
		"updated_at": row.UpdatedAt,
	})
}

func (ctl *ProviderController) ListProviders(c *gin.Context) {
	rows, err := ctl.svc.ListProviders(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		response.Fail(c, err)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, gin.H{
			"id":         row.ID,
			"name":       row.Name,
			"type":       row.Type,
			"base_url":   row.BaseURL,
			"test_model": row.TestModel,
			"status":     row.Status,
			"created_at": row.CreatedAt,
			"updated_at": row.UpdatedAt,
		})
	}
	response.Success(c, out)
}

func (ctl *ProviderController) CreateAIModel(c *gin.Context) {
	var req struct {
		ProviderID uint64         `json:"provider_id" binding:"required"`
		Name       string         `json:"name" binding:"required"`
		ModelID    string         `json:"model_id" binding:"required"`
		Params     map[string]any `json:"params"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid ai model request"))
		return
	}
	row, err := ctl.svc.CreateAIModel(c.Request.Context(), middleware.UserID(c), providerconfig.CreateAIModelInput{
		ProviderID: req.ProviderID,
		Name:       req.Name,
		ModelID:    req.ModelID,
		Params:     req.Params,
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, row)
}

func (ctl *ProviderController) ListAIModels(c *gin.Context) {
	rows, err := ctl.svc.ListAIModels(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, rows)
}
