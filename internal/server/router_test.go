package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"easygo-agent/internal/controller"
	"easygo-agent/internal/platform/auth"
	"easygo-agent/internal/platform/handler"
	"github.com/gin-gonic/gin"
)

// TestSkillRoutesRequireAuthentication verifies every management endpoint is inside the authorized route group.
func TestSkillRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	issuer, err := auth.NewIssuer("0123456789abcdef0123456789abcdef", auth.DefaultTTL)
	if err != nil {
		t.Fatalf("auth.NewIssuer() error = %v", err)
	}
	router := NewRouter(
		&handler.HealthHandler{},
		&controller.UserController{},
		&controller.AuthController{},
		&controller.ProviderController{},
		&controller.ChatController{},
		&controller.SkillController{},
		issuer,
	)

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/v1/skills"},
		{method: http.MethodGet, path: "/api/v1/skills"},
		{method: http.MethodDelete, path: "/api/v1/skills/user-guide"},
	} {
		request := httptest.NewRequest(route.method, route.path, nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", route.method, route.path, recorder.Code)
		}
	}
}
