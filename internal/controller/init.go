package controller

// AllControllers aggregates the HTTP adapters created by Wire.
type AllControllers struct {
	User     *UserController
	Auth     *AuthController
	Provider *ProviderController
	Chat     *ChatController
	Skill    *SkillController
}
