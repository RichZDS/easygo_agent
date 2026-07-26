package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/middleware"
	"easygo-agent/internal/response"
	"easygo-agent/internal/service/chat"

	"github.com/gin-gonic/gin"
)

type ChatTurnController struct {
	turns     *chat.TurnService
	execution *chat.ExecutionService
}

func NewChatTurnController(
	turns *chat.TurnService,
	execution *chat.ExecutionService,
) *ChatTurnController {
	return &ChatTurnController{turns: turns, execution: execution}
}

func (ctl *ChatTurnController) Stream(c *gin.Context) {
	var request struct {
		Input     string `json:"input" binding:"required"`
		RequestID string `json:"request_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid turn request"))
		return
	}

	prepared, err := ctl.turns.Prepare(
		c.Request.Context(),
		middleware.UserID(c),
		c.Param("id"),
		request.RequestID,
		request.Input,
	)
	if err != nil {
		var duplicate *chat.DuplicateRequestError
		if errors.As(err, &duplicate) {
			response.JSON(c, http.StatusConflict, errorcode.Conflict, gin.H{
				"turn_id": duplicate.Turn.TurnID,
				"status":  duplicate.Turn.Status,
			}, "duplicate_request")
			return
		}
		response.Fail(c, err)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	emitter := func(event chat.StreamEvent) error {
		var payload any = event.Payload
		if event.Type != "turn" {
			payload = gin.H{
				"turn_id": prepared.Turn.TurnID,
				"payload": event.Payload,
			}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal SSE payload: %w", err)
		}
		if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Type, data); err != nil {
			return err
		}
		c.Writer.Flush()
		return nil
	}

	if _, err := ctl.execution.Run(c.Request.Context(), prepared, emitter); err != nil {
		_ = c.Error(err)
	}
}

func (ctl *ChatTurnController) Get(c *gin.Context) {
	turn, err := ctl.turns.Get(
		c.Request.Context(),
		middleware.UserID(c),
		c.Param("turn_id"),
	)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, turn)
}

func (ctl *ChatTurnController) ListSessions(c *gin.Context) {
	sessions, err := ctl.turns.ListSessions(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, sessions)
}

func (ctl *ChatTurnController) ListMessages(c *gin.Context) {
	messages, err := ctl.turns.ListMessages(
		c.Request.Context(),
		middleware.UserID(c),
		c.Param("session_id"),
		200,
	)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, messages)
}

func (ctl *ChatTurnController) CreateSession(c *gin.Context) {
	var request struct {
		Title         string `json:"title" binding:"required"`
		ModelConfigID uint64 `json:"model_config_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid session request"))
		return
	}
	session, err := ctl.turns.CreateSession(
		c.Request.Context(),
		middleware.UserID(c),
		request.Title,
		request.ModelConfigID,
	)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, session)
}

func (ctl *ChatTurnController) SwitchModel(c *gin.Context) {
	var request struct {
		ModelConfigID uint64 `json:"model_config_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid model selection"))
		return
	}
	if err := ctl.turns.SwitchModel(
		c.Request.Context(),
		middleware.UserID(c),
		c.Param("id"),
		request.ModelConfigID,
	); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}
