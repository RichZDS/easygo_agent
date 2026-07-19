package controller

import (
	"net/http"
	"strconv"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/response"
	"easygo-agent/internal/service"

	"github.com/gin-gonic/gin"
)

type ChatSessionController struct {
	svc *service.ChatSessionService
}

func NewChatSessionController(svc *service.ChatSessionService) *ChatSessionController {
	return &ChatSessionController{svc: svc}
}

// ========== 请求/响应结构体 ==========

type CreateSessionRequest struct {
	UserID uint64 `json:"user_id" binding:"required"`
	Title  string `json:"title" binding:"required"`
}

type UpdateTitleRequest struct {
	Title string `json:"title" binding:"required"`
}

type UpdateSessionStatusRequest struct {
	Status uint8 `json:"status" binding:"required,min=1,max=3"`
}

// ========== HTTP 处理方法 ==========

// Create 创建会话
func (ctl *ChatSessionController) Create(c *gin.Context) {
	var req CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	session, err := ctl.svc.CreateSession(c.Request.Context(), req.UserID, req.Title)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, session)
}

// GetByID 根据主键 ID 查询
func (ctl *ChatSessionController) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "会话 ID 无效"))
		return
	}

	session, err := ctl.svc.FindByID(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, session)
}

// GetBySessionID 根据对外 session_id 查询
func (ctl *ChatSessionController) GetBySessionID(c *gin.Context) {
	sessionID := c.Query("session_id")
	if sessionID == "" {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "session_id 参数必填"))
		return
	}

	session, err := ctl.svc.FindBySessionID(c.Request.Context(), sessionID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, session)
}

// ListByUserID 查询某用户的所有会话
func (ctl *ChatSessionController) ListByUserID(c *gin.Context) {
	userID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "用户 ID 无效"))
		return
	}

	sessions, err := ctl.svc.ListByUserID(c.Request.Context(), userID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, sessions)
}

// UpdateTitle 更新会话标题
func (ctl *ChatSessionController) UpdateTitle(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "会话 ID 无效"))
		return
	}

	var req UpdateTitleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	if err := ctl.svc.UpdateTitle(c.Request.Context(), id, req.Title); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}

// UpdateStatus 更新会话状态
func (ctl *ChatSessionController) UpdateStatus(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "会话 ID 无效"))
		return
	}

	var req UpdateSessionStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	if err := ctl.svc.UpdateStatus(c.Request.Context(), id, req.Status); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}

// RecordMessage 记录消息（由内部消息系统调用）
func (ctl *ChatSessionController) RecordMessage(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "会话 ID 无效"))
		return
	}

	if err := ctl.svc.RecordMessage(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}

// Delete 软删除会话
func (ctl *ChatSessionController) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "会话 ID 无效"))
		return
	}

	if err := ctl.svc.SoftDelete(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
