package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"easygo-agent/internal/config"
	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/handler"
	"easygo-agent/internal/model"
	"easygo-agent/internal/response"
	"easygo-agent/internal/service"

	"go.uber.org/zap"
)

type fakeUserService struct{}

func (fakeUserService) Create(_ context.Context, input service.CreateUserInput) (*model.User, error) {
	return &model.User{ID: 1, Name: input.Name, Email: input.Email}, nil
}

func (fakeUserService) Get(_ context.Context, id uint64) (*model.User, error) {
	if id != 1 {
		return nil, errorcode.New(errorcode.NotFound, "用户不存在")
	}
	return &model.User{ID: 1, Name: "Ada", Email: "ada@example.com"}, nil
}

func testRouter() http.Handler {
	cfg := config.Config{}
	cfg.App.Env = "test"
	cfg.Security.AllowedOrigins = []string{"http://localhost:5173"}
	users := handler.NewUserHandler(fakeUserService{})
	health := handler.NewHealthHandler(map[string]handler.CheckFunc{
		"mysql": func(context.Context) error { return nil },
		"redis": func(context.Context) error { return nil },
	})
	return NewRouter(cfg, zap.NewNop(), users, health)
}

func TestCreateUserUsesGlobalResponse(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(`{"name":"Ada","email":"ada@example.com"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "test-request")
	responseRecorder := httptest.NewRecorder()

	testRouter().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", responseRecorder.Code, http.StatusCreated, responseRecorder.Body.String())
	}
	var body response.Body
	if err := json.Unmarshal(responseRecorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != errorcode.OK.Value || body.RequestID != "test-request" {
		t.Fatalf("unexpected response: %+v", body)
	}
	if got := responseRecorder.Header().Get("X-Request-ID"); got != "test-request" {
		t.Fatalf("X-Request-ID = %q", got)
	}
}

func TestInvalidUserReturnsBusinessCode(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(`{"name":"A","email":"bad"}`))
	request.Header.Set("Content-Type", "application/json")
	responseRecorder := httptest.NewRecorder()

	testRouter().ServeHTTP(responseRecorder, request)

	var body response.Body
	_ = json.Unmarshal(responseRecorder.Body.Bytes(), &body)
	if responseRecorder.Code != http.StatusBadRequest || body.Code != errorcode.InvalidParameter.Value {
		t.Fatalf("status=%d body=%+v", responseRecorder.Code, body)
	}
}

func TestNotFoundUsesGlobalResponse(t *testing.T) {
	responseRecorder := httptest.NewRecorder()
	testRouter().ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodGet, "/missing", nil))

	var body response.Body
	_ = json.Unmarshal(responseRecorder.Body.Bytes(), &body)
	if responseRecorder.Code != http.StatusNotFound || body.Code != errorcode.NotFound.Value {
		t.Fatalf("status=%d body=%+v", responseRecorder.Code, body)
	}
}
