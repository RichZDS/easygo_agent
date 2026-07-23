package controller

import "easygo-agent/internal/service/chat"

// AllControllers 聚合所有 Controller 和顶层 Service，供 Wire 依赖注入使用。
type AllControllers struct {
	User        *UserController
	Session     *ChatSessionController
	Message     *ChatMessageController
	Auth        *AuthController
	ModelConfig *ModelConfigController
	Turn        *ChatTurnController

	// 顶层 Service（未作为 Controller 依赖，但需 Wire 管理生命周期）
	AgentChat *chat.AgentChatService
}
