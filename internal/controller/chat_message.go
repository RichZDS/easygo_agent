package controller

import (
	"encoding/json"
	"net/http"
	"strconv"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/model"
	"easygo-agent/internal/response"
	"easygo-agent/internal/service/chat"

	"github.com/gin-gonic/gin"
)

type ChatMessageController struct {
	svc chat.ChatMessageService
}

func NewChatMessageController(svc chat.ChatMessageService) *ChatMessageController {
	return &ChatMessageController{svc: svc}
}

// ========== 请求/响应结构体 ==========

type CreateMessageRequest struct {
	ChatSessionID   uint64          `json:"chat_session_id" binding:"required"`
	TurnID          *string         `json:"turn_id"`
	ParentMessageID *string         `json:"parent_message_id"`
	Role            uint8           `json:"role" binding:"required,min=1,max=4"`
	MessageType     uint8           `json:"message_type"`
	Content         *string         `json:"content"`
	ModelName       *string         `json:"model_name"`
	ProviderName    *string         `json:"provider_name"`
	ToolCallID      *string         `json:"tool_call_id"`
	ToolName        *string         `json:"tool_name"`
	Status          uint8           `json:"status"`
	Metadata        json.RawMessage `json:"metadata"`
}

type ListMessagesResponse struct {
	Messages []model.ChatMessage `json:"messages"`
	Total    int64               `json:"total"`
}

type UpdateMessageStatusRequest struct {
	Status       uint8   `json:"status" binding:"required,min=1,max=4"`
	FinishReason *string `json:"finish_reason"`
}

type UpdateContentRequest struct {
	Content string `json:"content" binding:"required"`
}

type UpdateTokensRequest struct {
	PromptTokens     uint32 `json:"prompt_tokens" binding:"required"`
	CompletionTokens uint32 `json:"completion_tokens" binding:"required"`
	TotalTokens      uint32 `json:"total_tokens" binding:"required"`
}

type SetErrorRequest struct {
	ErrorCode    string `json:"error_code" binding:"required"`
	ErrorMessage string `json:"error_message" binding:"required"`
}

// ========== HTTP 处理方法 ==========

// Create 创建消息
func (ctl *ChatMessageController) Create(c *gin.Context) {
	var req CreateMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	// 从 Gin 上下文获取 request_id（由 RequestID 中间件注入）
	requestID, _ := c.Get(response.RequestIDKey)
	var reqIDPtr *string
	if rid, ok := requestID.(string); ok && rid != "" {
		reqIDPtr = &rid
	}

	// 解析 metadata
	var metadata *model.JSONMap
	if len(req.Metadata) > 0 {
		m := make(model.JSONMap)
		if err := json.Unmarshal(req.Metadata, &m); err != nil {
			response.Fail(c, errorcode.New(errorcode.InvalidParameter, "metadata 格式错误: "+err.Error()))
			return
		}
		metadata = &m
	}

	msg, err := ctl.svc.CreateMessage(c.Request.Context(), &chat.CreateMessageParams{
		ChatSessionID:   req.ChatSessionID,
		TurnID:          req.TurnID,
		ParentMessageID: req.ParentMessageID,
		Role:            req.Role,
		MessageType:     req.MessageType,
		Content:         req.Content,
		ModelName:       req.ModelName,
		ProviderName:    req.ProviderName,
		ToolCallID:      req.ToolCallID,
		ToolName:        req.ToolName,
		Status:          req.Status,
		RequestID:       reqIDPtr,
		Metadata:        metadata,
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, msg)
}

// GetByID 根据主键 ID 查询
func (ctl *ChatMessageController) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "消息 ID 无效"))
		return
	}

	msg, err := ctl.svc.FindByID(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, msg)
}

// GetByMessageID 根据对外 message_id 查询
func (ctl *ChatMessageController) GetByMessageID(c *gin.Context) {
	messageID := c.Query("message_id")
	if messageID == "" {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "message_id 参数必填"))
		return
	}

	msg, err := ctl.svc.FindByMessageID(c.Request.Context(), messageID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, msg)
}

// ListBySessionID 分页查询会话内消息
func (ctl *ChatMessageController) ListBySessionID(c *gin.Context) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "会话 ID 无效"))
		return
	}

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	messages, err := ctl.svc.ListBySessionID(c.Request.Context(), sessionID, offset, limit)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, messages)
}

// UpdateStatus 更新消息状态
func (ctl *ChatMessageController) UpdateStatus(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "消息 ID 无效"))
		return
	}

	var req UpdateMessageStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	if err := ctl.svc.UpdateStatus(c.Request.Context(), id, req.Status, req.FinishReason); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}

// UpdateContent 更新消息内容
func (ctl *ChatMessageController) UpdateContent(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "消息 ID 无效"))
		return
	}

	var req UpdateContentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	if err := ctl.svc.UpdateContent(c.Request.Context(), id, req.Content); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}

// UpdateTokens 更新 Token 统计
func (ctl *ChatMessageController) UpdateTokens(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "消息 ID 无效"))
		return
	}

	var req UpdateTokensRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	if err := ctl.svc.UpdateTokens(c.Request.Context(), id, req.PromptTokens, req.CompletionTokens, req.TotalTokens); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}

// SetError 记录错误
func (ctl *ChatMessageController) SetError(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "消息 ID 无效"))
		return
	}

	var req SetErrorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	if err := ctl.svc.SetError(c.Request.Context(), id, req.ErrorCode, req.ErrorMessage); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}

// Delete 软删除
func (ctl *ChatMessageController) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "消息 ID 无效"))
		return
	}

	if err := ctl.svc.SoftDelete(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
