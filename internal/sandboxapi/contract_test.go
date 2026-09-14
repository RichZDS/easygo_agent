package sandboxapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplicationPathRejectsTraversal(t *testing.T) {
	if _, err := ApplicationPath("../bad", "/exec"); err == nil {
		t.Fatal("traversal application id was accepted")
	}
	path, err := ApplicationPath("app_ok", "/exec")
	if err != nil || path != ApplicationsPath+"/app_ok/exec" {
		t.Fatalf("path=%s err=%v", path, err)
	}
}

func TestClientRequiresIdentityAndParsesErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(SessionHeader) == "" || r.Header.Get(RunHeader) == "" {
			t.Error("client omitted identity headers")
		}
		w.Header().Set("Retry-After", "3")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "capacity_exhausted", "message": "all sandboxes are busy"}})
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, Token: "token", HTTP: server.Client()}
	err := client.Do(context.Background(), http.MethodPost, ApplicationsPath, "", "run-1", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "trusted session and run identity") {
		t.Fatalf("missing identity error=%v", err)
	}
	err = client.Do(context.Background(), http.MethodPost, ApplicationsPath, "session-1", "run-1", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "capacity_exhausted: all sandboxes are busy (retry after 3)") {
		t.Fatalf("error envelope=%v", err)
	}
}
