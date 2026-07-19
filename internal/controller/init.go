package controller

import (
	"easygo-agent/internal/service"
	"easygo-agent/internal/service/chat"

	"gorm.io/gorm"
)

// AllControllers 聚合所有 Controller，由 InitAll 一次性创建。
type AllControllers struct {
	User    *UserController
	Session *ChatSessionController
	Message *ChatMessageController
}

// InitAll 初始化所有 Service 和 Controller，返回聚合结构体。
func InitAll(db *gorm.DB) *AllControllers {
	userSvc := service.NewUserService(db)
	sessionSvc := chat.NewChatSessionService(db)
	msgSvc := chat.NewChatMessageService(db)

	return &AllControllers{
		User:    NewUserController(userSvc),
		Session: NewChatSessionController(sessionSvc),
		Message: NewChatMessageController(msgSvc),
	}
}
