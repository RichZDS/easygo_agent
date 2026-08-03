package controller

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"easygo-agent/internal/config"
	"easygo-agent/internal/platform/middleware"
	"easygo-agent/internal/platform/response"
	skillstore "easygo-agent/internal/skill/store"
	"easygo-agent/internal/skill/workspace"
	"github.com/gin-gonic/gin"
)

// TestSkillControllerLifecycle verifies authenticated upload, listing, and deletion responses.
func TestSkillControllerLifecycle(t *testing.T) {
	controller, rootDir := newSkillControllerFixture(t, 5<<20)
	writeControllerSkill(t, filepath.Join(rootDir, "builtin", "builtin-guide"), "builtin-guide")
	router := skillControllerRouter(controller, 91)

	upload := performSkillUpload(t, router, "user-guide", controllerSkillArchive(t, "user-guide"))
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", upload.Code, upload.Body.String())
	}

	list := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/skills", nil)
	router.ServeHTTP(list, request)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	var body response.Body
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	items, ok := body.Data.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("list data = %#v, want two skills", body.Data)
	}

	deleted := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodDelete, "/api/v1/skills/user-guide", nil)
	router.ServeHTTP(deleted, request)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
}

// TestSkillControllerMapsStoreErrors verifies stable HTTP semantics for invalid, limited, and protected mutations.
func TestSkillControllerMapsStoreErrors(t *testing.T) {
	controller, rootDir := newSkillControllerFixture(t, 128)
	writeControllerSkill(t, filepath.Join(rootDir, "builtin", "builtin-guide"), "builtin-guide")
	router := skillControllerRouter(controller, 92)

	invalid := performSkillUpload(t, router, "bad-skill", []byte("not a zip"))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid upload status = %d, want 400", invalid.Code)
	}
	oversized := performSkillUpload(t, router, "large-skill", bytes.Repeat([]byte("x"), 256))
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload status = %d, want 413", oversized.Code)
	}

	forbidden := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/skills/builtin-guide", nil)
	router.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("builtin delete status = %d, want 403", forbidden.Code)
	}
	missing := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodDelete, "/api/v1/skills/missing-skill", nil)
	router.ServeHTTP(missing, request)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing delete status = %d, want 404", missing.Code)
	}
}

// newSkillControllerFixture creates a real Store-backed controller in an isolated root.
func newSkillControllerFixture(t *testing.T, maxZipBytes int64) (*SkillController, string) {
	t.Helper()
	rootDir := filepath.Join(t.TempDir(), "skills")
	cfg := config.Skills{RootDir: rootDir, ReadmeSrc: "README.md", MaxZipBytes: maxZipBytes, MaxExtractedBytes: 20 << 20, MaxFiles: 200}
	manager, err := workspace.NewManager(cfg)
	if err != nil {
		t.Fatalf("workspace.NewManager() error = %v", err)
	}
	store, err := skillstore.New(cfg, manager)
	if err != nil {
		t.Fatalf("skillstore.New() error = %v", err)
	}
	return NewSkillController(store), rootDir
}

// skillControllerRouter creates direct authenticated routes for controller behavior tests.
func skillControllerRouter(controller *SkillController, userID uint64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	// setTestUser injects the already-authenticated user identity expected by the controller.
	setTestUser := func(c *gin.Context) {
		c.Set(middleware.UserIDKey, userID)
		c.Next()
	}
	router.Use(setTestUser)
	router.POST("/api/v1/skills", controller.Upload)
	router.GET("/api/v1/skills", controller.List)
	router.DELETE("/api/v1/skills/:skill_id", controller.Delete)
	return router
}

// performSkillUpload sends one multipart upload through the controller router.
func performSkillUpload(t *testing.T, router *gin.Engine, skillID string, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("skill_id", skillID); err != nil {
		t.Fatalf("WriteField() error = %v", err)
	}
	file, err := writer.CreateFormFile("file", "skill.zip")
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}
	if _, err := file.Write(archive); err != nil {
		t.Fatalf("multipart file Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart Close() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/skills", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// controllerSkillArchive creates one valid root-layout ZIP for HTTP tests.
func controllerSkillArchive(t *testing.T, skillID string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("SKILL.md")
	if err != nil {
		t.Fatalf("zip.Create() error = %v", err)
	}
	content := "---\nname: " + skillID + "\ndescription: Controller skill\n---\nbody"
	if _, err := entry.Write([]byte(content)); err != nil {
		t.Fatalf("zip entry Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip.Close() error = %v", err)
	}
	return buffer.Bytes()
}

// writeControllerSkill creates one valid builtin skill for controller tests.
func writeControllerSkill(t *testing.T, skillDir, skillID string) {
	t.Helper()
	if err := os.MkdirAll(skillDir, 0o750); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	content := "---\nname: " + skillID + "\ndescription: Builtin controller skill\n---\nbody"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
}
