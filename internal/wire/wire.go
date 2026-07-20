//go:build wireinject
// +build wireinject

package wire

import (
	"github.com/google/wire"
	"gorm.io/gorm"

	"easygo-agent/internal/controller"
	chatcache "easygo-agent/internal/repository/chatcache"
	"easygo-agent/internal/service/chat"
	"easygo-agent/internal/service/user"
)

func InitControllers(db *gorm.DB, cacheCfg chatcache.Config) *controller.AllControllers {
	wire.Build(
		// Repositories
		chatcache.NewChatCacheRepo,

		// Services
		user.NewUserService,
		chat.NewChatSessionService,
		chat.NewChatMessageService,
		chat.NewAgentChatService,

		// Controllers
		controller.NewUserController,
		controller.NewChatSessionController,
		controller.NewChatMessageController,

		// Struct aggregation
		wire.Struct(new(controller.AllControllers), "*"),
	)
	return nil
}
