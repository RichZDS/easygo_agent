package controller

// AllControllers 聚合所有 Controller，供 Wire 依赖注入使用。
type AllControllers struct {
	User    *UserController
	Session *ChatSessionController
	Message *ChatMessageController
}
