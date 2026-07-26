package controller

// AllControllers aggregates the HTTP adapters created by Wire.
type AllControllers struct {
	User        *UserController
	Auth        *AuthController
	ModelConfig *ModelConfigController
	Turn        *ChatTurnController
}
