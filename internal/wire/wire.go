//go:build wireinject
// +build wireinject

package wire

import (
	"github.com/google/wire"
	"gorm.io/gorm"

	"easygo-agent/internal/controller"
	"easygo-agent/internal/service/chat"
	"easygo-agent/internal/service/user"
)

func InitControllers(db *gorm.DB) *controller.AllControllers {
	wire.Build(
		// Services
		user.NewUserService,
		chat.NewChatSessionService,
		chat.NewChatMessageService,

		// Controllers
		controller.NewUserController,
		controller.NewChatSessionController,
		controller.NewChatMessageController,

		// Struct aggregation
		wire.Struct(new(controller.AllControllers), "*"),
	)
	return nil
}
