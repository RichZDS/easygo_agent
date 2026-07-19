package handler

import (
	"strconv"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/response"
	"easygo-agent/internal/service"

	"github.com/gin-gonic/gin"
)

type UserHandler struct {
	users service.UserService
}

func NewUserHandler(users service.UserService) *UserHandler {
	return &UserHandler{users: users}
}

type createUserRequest struct {
	Name  string `json:"name" binding:"required,min=2,max=64"`
	Email string `json:"email" binding:"required,email,max=191"`
}

func (h *UserHandler) Create(c *gin.Context) {
	var request createUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "姓名或邮箱格式不正确"))
		return
	}

	user, err := h.users.Create(c.Request.Context(), service.CreateUserInput{
		Name:  request.Name,
		Email: request.Email,
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, user)
}

func (h *UserHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "用户 ID 必须是正整数"))
		return
	}

	user, err := h.users.Get(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, user)
}
