package errorcode

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is the stable business code consumed by the frontend. HTTPStatus is
// still used for proxies and monitoring; clients should branch on Code.
type Code struct {
	Value      int
	HTTPStatus int
	Message    string
}

var (
	OK               = Code{Value: 0, HTTPStatus: http.StatusOK, Message: "success"}
	InvalidParameter = Code{Value: 10001, HTTPStatus: http.StatusBadRequest, Message: "请求参数错误"}
	Unauthorized     = Code{Value: 10002, HTTPStatus: http.StatusUnauthorized, Message: "未登录或登录已失效"}
	Forbidden        = Code{Value: 10003, HTTPStatus: http.StatusForbidden, Message: "无权执行此操作"}
	NotFound         = Code{Value: 10004, HTTPStatus: http.StatusNotFound, Message: "资源不存在"}
	Conflict         = Code{Value: 10009, HTTPStatus: http.StatusConflict, Message: "资源已存在"}
	Database         = Code{Value: 20001, HTTPStatus: http.StatusInternalServerError, Message: "数据库操作失败"}
	Cache            = Code{Value: 20002, HTTPStatus: http.StatusInternalServerError, Message: "缓存操作失败"}
	Internal         = Code{Value: 50000, HTTPStatus: http.StatusInternalServerError, Message: "服务器内部错误"}
)

type Error struct {
	Code    Code
	Message string
	Cause   error
}

func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func Wrap(code Code, cause error) *Error {
	return &Error{Code: code, Cause: cause}
}

// 创建一个错误
func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func (e *Error) Error() string {
	message := e.Message
	if message == "" {
		message = e.Code.Message
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", message, e.Cause)
	}
	return message
}

func (e *Error) Unwrap() error { return e.Cause }

func From(err error) *Error {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	return Wrap(Internal, err)
}
