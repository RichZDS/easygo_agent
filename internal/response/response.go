package response

import (
	"net/http"

	"easygo-agent/internal/errorcode"

	"github.com/gin-gonic/gin"
)

// RequestIDKey 是存储在 gin.Context 中的 request_id 的键名。
// 由 middleware.RequestID 中间件注入，供 response 和 controller 层使用。
const RequestIDKey = "request_id"

type Body struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data"`
	RequestID string `json:"request_id,omitempty"`
}

func Success(c *gin.Context, data any) {
	JSON(c, http.StatusOK, errorcode.OK, data, "")
}

func Created(c *gin.Context, data any) {
	JSON(c, http.StatusCreated, errorcode.OK, data, "")
}

func Fail(c *gin.Context, err error) {
	_ = c.Error(err)
	appErr := errorcode.From(err)
	message := appErr.Message
	if message == "" {
		message = appErr.Code.Message
	}
	JSON(c, appErr.Code.HTTPStatus, appErr.Code, nil, message)
}

func JSON(c *gin.Context, status int, code errorcode.Code, data any, message string) {
	if message == "" {
		message = code.Message
	}
	requestID, _ := c.Get(RequestIDKey)
	c.JSON(status, Body{
		Code:      code.Value,
		Message:   message,
		Data:      data,
		RequestID: stringValue(requestID),
	})
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
