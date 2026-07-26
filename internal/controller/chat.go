package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/platform/middleware"
	"easygo-agent/internal/platform/response"
	"easygo-agent/internal/service/chat"

	"github.com/gin-gonic/gin"
)

type ChatController struct {
	runs      *chat.RunService
	execution *chat.ExecutionService
}

func NewChatController(runs *chat.RunService, execution *chat.ExecutionService) *ChatController {
	return &ChatController{runs: runs, execution: execution}
}

func (ctl *ChatController) Stream(c *gin.Context) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || sessionID == 0 {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid session id"))
		return
	}
	var request struct {
		Input     string `json:"input" binding:"required"`
		RequestID string `json:"request_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid run request"))
		return
	}

	prepared, err := ctl.runs.Prepare(
		c.Request.Context(),
		middleware.UserID(c),
		sessionID,
		request.RequestID,
		request.Input,
	)
	if err != nil {
		var duplicate *chat.DuplicateRequestError
		if errors.As(err, &duplicate) {
			response.JSON(c, http.StatusConflict, errorcode.Conflict, gin.H{
				"run_id": duplicate.Run.ID,
				"status": duplicate.Run.Status,
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
		if event.Type != "run" {
			payload = gin.H{
				"run_id":  prepared.Run.ID,
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

func (ctl *ChatController) GetRun(c *gin.Context) {
	runID, err := strconv.ParseUint(c.Param("run_id"), 10, 64)
	if err != nil || runID == 0 {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid run id"))
		return
	}
	run, err := ctl.runs.GetRun(c.Request.Context(), middleware.UserID(c), runID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, run)
}

func (ctl *ChatController) ListSessions(c *gin.Context) {
	sessions, err := ctl.runs.ListSessions(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, sessions)
}

func (ctl *ChatController) ListMessages(c *gin.Context) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || sessionID == 0 {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid session id"))
		return
	}
	messages, err := ctl.runs.ListMessages(c.Request.Context(), middleware.UserID(c), sessionID, 200)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, messages)
}

func (ctl *ChatController) CreateSession(c *gin.Context) {
	var request struct {
		Title     string `json:"title" binding:"required"`
		AIModelID uint64 `json:"ai_model_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid session request"))
		return
	}
	session, err := ctl.runs.CreateSession(
		c.Request.Context(),
		middleware.UserID(c),
		request.Title,
		request.AIModelID,
	)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, session)
}

func (ctl *ChatController) SwitchModel(c *gin.Context) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || sessionID == 0 {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid session id"))
		return
	}
	var request struct {
		AIModelID uint64 `json:"ai_model_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "invalid model selection"))
		return
	}
	if err := ctl.runs.SwitchModel(
		c.Request.Context(),
		middleware.UserID(c),
		sessionID,
		request.AIModelID,
	); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}
