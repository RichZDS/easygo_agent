package chatmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/internal/config"
	"easygo-agent/services/ai-gateway/ai"
	"easygo-agent/services/ai-gateway/gateway"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func remoteModelConfig(t *testing.T, endpoint string) config.ModelConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf(`model:
  protocol: easygo
  name: remote-chat
  endpoint: %s
  apikey: "{REMOTE_GATEWAY_KEY}"
  timeout: 2s
  parameters:
    top_p: 0.7
    seed: 42
    temperature: 0.1
    max_output_tokens: 20
    tool_choice: none
database:
  driver: memory
memory:
  enabled: false
`, endpoint)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path, func(key string) (string, bool) { return "gateway-key", key == "REMOTE_GATEWAY_KEY" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Model
}

func TestRemoteGatewayYAMLHTTPDefaultsOptionsAndStreaming(t *testing.T) {
	for _, mode := range []string{"defaults", "override", "stream"} {
		t.Run(mode, func(t *testing.T) {
			providerRequests := make(chan map[string]any, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer upstream-key" {
					t.Error("remote gateway did not use provider credential")
				}
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				providerRequests <- req
				if req["stream"] == true {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"remote answer\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"remote answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
				}
			}))
			defer provider.Close()
			g, err := gateway.New(gateway.Config{Models: map[string]gateway.Model{"remote-chat": {Protocol: "chat_completions", Endpoint: provider.URL + "/chat", APIKey: "upstream-key", Model: "upstream-id", Parameters: map[string]json.RawMessage{"top_p": json.RawMessage(`0.9`)}, Price: &gateway.Pricing{Currency: "USD", InputPerMillion: 2, OutputPerMillion: 4}}}})
			if err != nil {
				t.Fatal(err)
			}
			handler := gateway.NewHandler(g, gateway.HandlerConfig{BearerToken: "gateway-key"})
			canonicalRequests := make(chan gateway.GenerateRequest, 1)
			var networkCalls atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				networkCalls.Add(1)
				if r.URL.Path != "/v1/generate" || r.Method != http.MethodPost {
					t.Errorf("unexpected discovery/path: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer gateway-key" {
					t.Error("gateway bearer missing")
				}
				raw, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(raw))
				var request gateway.GenerateRequest
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Error(err)
				}
				canonicalRequests <- request
				handler.ServeHTTP(w, r)
			}))
			defer remote.Close()
			cfg := remoteModelConfig(t, remote.URL+"/v1/generate")
			adapter, err := New(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if networkCalls.Load() != 0 {
				t.Fatal("startup performed gateway discovery")
			}
			// Constructed defaults must not alias the caller's mutable configuration.
			cfg.Parameters["seed"][0] = '9'
			var opts []model.Option
			if mode != "defaults" {
				opts = []model.Option{WithParameters(map[string]json.RawMessage{"seed": json.RawMessage(`84`), "top_p": json.RawMessage(`0.6`), "max_output_tokens": json.RawMessage(`30`)}), model.WithTopP(0.25), model.WithTemperature(0.5), model.WithMaxTokens(12), model.WithToolChoice(schema.ToolChoiceAllowed)}
			}
			var out *schema.AgenticMessage
			if mode == "stream" {
				reader, e := adapter.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("hello")}, opts...)
				if e != nil {
					t.Fatal(e)
				}
				out, err = drain(reader)
			} else {
				out, err = adapter.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("hello")}, opts...)
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.ContentBlocks[0].AssistantGenText.Text != "remote answer" || out.ResponseMeta.TokenUsage.TotalTokens != 12 {
				t.Fatalf("bad output: %+v", out)
			}
			var metadata struct {
				Cost ai.Cost `json:"cost"`
			}
			encoded, _ := json.Marshal(out.ResponseMeta.Extension)
			if err = json.Unmarshal(encoded, &metadata); err != nil {
				t.Fatal(err)
			}
			if !metadata.Cost.Known || metadata.Cost.Currency != "USD" || metadata.Cost.Amount != 0.000028 {
				t.Fatalf("remote cost lost/recomputed: %+v", metadata.Cost)
			}
			canonical := <-canonicalRequests
			if canonical.Model != "remote-chat" || canonical.Stream != (mode == "stream") {
				t.Fatalf("remote alias/stream lost: %+v", canonical)
			}
			upstream := <-providerRequests
			choice := "none"
			seed, topP, limit, temperature := float64(42), float64(0.7), float64(20), float64(0.1)
			if mode != "defaults" {
				seed, topP, limit, temperature = 84, 0.25, 12, 0.5
				choice = "auto"
			}
			if upstream["model"] != "upstream-id" || upstream["seed"] != seed || upstream["top_p"] != topP || upstream["max_completion_tokens"] != limit || upstream["temperature"] != temperature || upstream["tool_choice"] != choice {
				t.Fatalf("defaults/overrides mapped incorrectly: %+v", upstream)
			}
			if networkCalls.Load() != 1 {
				t.Fatalf("unexpected extra calls: %d", networkCalls.Load())
			}
		})
	}
}

func TestRemoteGatewayContextAndConfiguredTimeout(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			started, finished := make(chan struct{}), make(chan struct{})
			release := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				select {
				case <-r.Context().Done():
				case <-release:
				}
				close(finished)
			}))
			defer provider.Close()
			defer close(release)
			g, err := gateway.New(gateway.Config{Models: map[string]gateway.Model{"remote-chat": {Protocol: "chat_completions", Endpoint: provider.URL, Model: "upstream"}}})
			if err != nil {
				t.Fatal(err)
			}
			remote := httptest.NewServer(gateway.NewHandler(g, gateway.HandlerConfig{BearerToken: "gateway-key"}))
			defer remote.Close()
			cfg := remoteModelConfig(t, remote.URL+"/v1/generate")
			if mode == "timeout" {
				cfg.Timeout = 50 * time.Millisecond
			}
			a, err := New(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			completed := make(chan error, 1)
			go func() {
				_, err := a.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("wait")})
				completed <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("provider not called")
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-completed:
				if err == nil {
					t.Fatal("canceled/timed-out request succeeded")
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation cause lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("request did not terminate")
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("upstream remained alive")
			}
		})
	}
}

func TestRemoteGatewayConstructorRejectsProviderOwnedSettings(t *testing.T) {
	for _, cfg := range []config.ModelConfig{
		{Protocol: "easygo", Name: "alias", BaseURL: "http://localhost/v1"},
		{Protocol: "easygo", Name: "alias", Endpoint: "http://localhost/v1/generate", Pricing: &config.ModelPricing{Currency: "USD"}},
		{Protocol: "easygo", Name: "alias", Endpoint: "http://localhost/v1/generate", ParameterMap: map[string]string{}},
	} {
		if _, err := New(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "model.") {
			t.Fatalf("invalid remote config accepted: %v", err)
		}
	}
}
