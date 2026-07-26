package controller

import (
	"net/http"
	"strconv"

	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/response"
	"easygo-agent/internal/service/user"

	"github.com/gin-gonic/gin"
)

type UserController struct {
	svc user.UserService
}

func NewUserController(svc user.UserService) *UserController {
	return &UserController{svc: svc}
}

// ========== 请求/响应结构体 ==========

type CreateUserRequest struct {
	Name     string  `json:"name" binding:"required"`
	Password string  `json:"password" binding:"required"`
	Email    *string `json:"email"`
	Phone    *string `json:"phone"`
}

type UpdateContactRequest struct {
	Email *string `json:"email"`
	Phone *string `json:"phone"`
}

type UpdatePasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// ========== HTTP 处理方法 ==========

// Create 创建用户
func (ctl *UserController) Create(c *gin.Context) {
	var req CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	user, err := ctl.svc.CreateUser(c.Request.Context(), req.Name, req.Password, req.Email, req.Phone)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, user)
}

// GetByID 根据 ID 查询用户
func (ctl *UserController) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "用户 ID 无效"))
		return
	}

	user, err := ctl.svc.FindUserByID(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, user)
}

// GetByName 根据名称查询用户
func (ctl *UserController) GetByName(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "name 参数必填"))
		return
	}

	user, err := ctl.svc.FindUserByName(c.Request.Context(), name)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, user)
}

// UpdateContact 更新联系方式
func (ctl *UserController) UpdateContact(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "用户 ID 无效"))
		return
	}

	var req UpdateContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	if err := ctl.svc.UpdateContact(c.Request.Context(), id, req.Email, req.Phone); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}

// UpdatePassword 更新密码
func (ctl *UserController) UpdatePassword(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "用户 ID 无效"))
		return
	}

	var req UpdatePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "参数错误: "+err.Error()))
		return
	}

	if err := ctl.svc.UpdatePassword(c.Request.Context(), id, req.Password); err != nil {
		response.Fail(c, err)
		return
	}
	response.Success(c, nil)
}

// Delete 软删除用户
func (ctl *UserController) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errorcode.New(errorcode.InvalidParameter, "用户 ID 无效"))
		return
	}

	if err := ctl.svc.SoftDeleteUser(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
