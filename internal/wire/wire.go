//go:build wireinject
// +build wireinject

package wire

import (
	agentframework "easygo-agent/internal/agent"
	"easygo-agent/internal/controller"
	"easygo-agent/internal/credential"
	"easygo-agent/internal/platform/auth"
	"easygo-agent/internal/service/account"
	"easygo-agent/internal/service/chat"
	"easygo-agent/internal/service/providerconfig"
	"easygo-agent/internal/service/user"
	skillstore "easygo-agent/internal/skill/store"

	"github.com/google/wire"
	"gorm.io/gorm"
)

func InitControllers(
	db *gorm.DB,
	cipher *credential.Cipher,
	issuer *auth.Issuer,
	runtime *agentframework.RuntimeFactory,
	skills *skillstore.Store,
) *controller.AllControllers {
	wire.Build(
		user.NewUserService,
		account.New,
		providerconfig.New,
		chat.NewRunService,
		chat.NewExecutionService,
		controller.NewUserController,
		controller.NewAuthController,
		controller.NewProviderController,
		controller.NewChatController,
		controller.NewSkillController,
		wire.Struct(new(controller.AllControllers), "*"),
	)
	return nil
}
