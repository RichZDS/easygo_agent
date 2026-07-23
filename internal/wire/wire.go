//go:build wireinject
// +build wireinject

package wire

import (
	"easygo-agent/internal/auth"
	"easygo-agent/internal/credential"
	"github.com/google/wire"
	"gorm.io/gorm"

	"easygo-agent/internal/controller"
	chatcache "easygo-agent/internal/repository/chatcache"
	"easygo-agent/internal/service/account"
	"easygo-agent/internal/service/chat"
	"easygo-agent/internal/service/modelconfig"
	"easygo-agent/internal/service/user"
)

func InitControllers(db *gorm.DB, cacheCfg chatcache.Config, cipher *credential.Cipher, issuer *auth.Issuer) *controller.AllControllers {
	wire.Build(
		// Repositories
		chatcache.NewChatCacheRepo,

		// Services
		user.NewUserService,
		account.New,
		modelconfig.New,
		chat.NewChatSessionService,
		chat.NewChatMessageService,
		chat.NewAgentChatService,
		chat.NewTurnService,

		// Controllers
		controller.NewUserController,
		controller.NewChatSessionController,
		controller.NewChatMessageController,
		controller.NewAuthController,
		controller.NewModelConfigController,
		controller.NewChatTurnController,

		// Struct aggregation
		wire.Struct(new(controller.AllControllers), "*"),
	)
	return nil
}
