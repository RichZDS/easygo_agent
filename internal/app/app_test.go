package app

import (
	"context"
	"easygo-agent/internal/logger"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOneShotCLIWithOpenAICompatibleServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected model path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "fake-model" || body["stream"] != true {
			t.Errorf("unexpected model request: %v", body)
		}
		if r.Header.Get("Authorization") != "Bearer fake-key" {
			t.Error("model credential config was not applied")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello from CLI\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := fmt.Sprintf("model:\n  name: fake-model\n  base_url: %q\n  apikey: '{FAKE_KEY}'\ndatabase:\n  driver: memory\n", server.URL)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	oldLookup := lookupEnv
	lookupEnv = func(string) (string, bool) { return "fake-key", true }
	defer func() { lookupEnv = oldLookup; logger.Sync(); zap.ReplaceGlobals(zap.NewNop()) }()
	if err := Run(ctx, path, Options{Mode: "cli", Username: "test-cli", Input: "say hello"}); err != nil {
		t.Fatal(err)
	}
}
