package controller

import (
	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/middleware"
	redisplatform "easygo-agent/internal/platform/redis"
	"easygo-agent/internal/response"
	"easygo-agent/internal/service/chat"
	"fmt"
	"github.com/gin-gonic/gin"
	redisclient "github.com/redis/go-redis/v9"
	"net/http"
	"time"
)

type ChatTurnController struct{ service *chat.TurnService }

func NewChatTurnController(service *chat.TurnService) *ChatTurnController {
	return &ChatTurnController{service}
}
func (ctl *ChatTurnController) Create(c *gin.Context) {
	var req struct {
		Input     string `json:"input" binding:"required"`
		RequestID string `json:"request_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid turn request"))
		return
	}
	turn, err := ctl.service.Create(c.Request.Context(), middleware.UserID(c), c.Param("session_id"), req.RequestID, req.Input)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.JSON(c, http.StatusAccepted, errorcode.OK, gin.H{"turn_id": turn.TurnID, "status": turn.Status}, "")
}
func (ctl *ChatTurnController) Get(c *gin.Context) {
	turn, err := ctl.service.Get(c.Request.Context(), middleware.UserID(c), c.Param("turn_id"))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, turn)
}
func (ctl *ChatTurnController) Events(c *gin.Context) {
	if _, err := ctl.service.Get(c.Request.Context(), middleware.UserID(c), c.Param("turn_id")); err != nil {
		response.Fail(c, err)
		return
	}
	lastID := c.GetHeader("Last-Event-ID")
	if lastID == "" {
		lastID = "0-0"
	}
	stream := "easygo:chat:turn:" + c.Param("turn_id") + ":events"
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Status(http.StatusOK)
	_, _ = c.Writer.WriteString(": connected\n\n")
	c.Writer.Flush()
	for {
		if c.Request.Context().Err() != nil {
			return
		}
		streams, err := redisplatform.XRead(c.Request.Context(), &redisclient.XReadArgs{Streams: []string{stream, lastID}, Block: 5 * time.Second, Count: 100})
		if err != nil {
			continue
		}
		for _, item := range streams {
			for _, message := range item.Messages {
				event, _ := message.Values["event"].(string)
				data, _ := message.Values["data"].(string)
				_, _ = fmt.Fprintf(c.Writer, "id: %s\nevent: %s\ndata: %s\n\n", message.ID, event, data)
				c.Writer.Flush()
				lastID = message.ID
			}
		}
	}
}
func (ctl *ChatTurnController) CreateSession(c *gin.Context) {
	var req struct {
		Title         string `json:"title" binding:"required"`
		ModelConfigID uint64 `json:"model_config_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid session request"))
		return
	}
	v, err := ctl.service.CreateSession(c.Request.Context(), middleware.UserID(c), req.Title, req.ModelConfigID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, v)
}
func (ctl *ChatTurnController) SwitchModel(c *gin.Context) {
	var req struct {
		ModelConfigID uint64 `json:"model_config_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid model selection"))
		return
	}
	if err := ctl.service.SwitchModel(c.Request.Context(), middleware.UserID(c), c.Param("session_id"), req.ModelConfigID); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}
