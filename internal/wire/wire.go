//go:build wireinject
// +build wireinject

package wire

import (
	agentframework "easygo-agent/internal/agent"
	"easygo-agent/internal/auth"
	"easygo-agent/internal/controller"
	"easygo-agent/internal/credential"
	"easygo-agent/internal/service/account"
	"easygo-agent/internal/service/chat"
	"easygo-agent/internal/service/modelconfig"
	"easygo-agent/internal/service/user"

	"github.com/google/wire"
	"gorm.io/gorm"
)

func InitControllers(
	db *gorm.DB,
	cipher *credential.Cipher,
	issuer *auth.Issuer,
	runtime *agentframework.RuntimeFactory,
) *controller.AllControllers {
	wire.Build(
		user.NewUserService,
		account.New,
		modelconfig.New,
		chat.NewTurnService,
		chat.NewExecutionService,
		controller.NewUserController,
		controller.NewAuthController,
		controller.NewModelConfigController,
		controller.NewChatTurnController,
		wire.Struct(new(controller.AllControllers), "*"),
	)
	return nil
}
