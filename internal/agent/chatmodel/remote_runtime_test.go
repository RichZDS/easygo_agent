package chatmodel_test

import (
	"bytes"
	"context"
	"encoding/json"
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

	"easygo-agent/internal/agent/chatmodel"
	"easygo-agent/internal/agent/deepagent"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/tools"
	"easygo-agent/services/ai-gateway/ai"
	"easygo-agent/services/ai-gateway/gateway"
	"github.com/cloudwego/eino/components/tool"
)

func customRemoteModel(t *testing.T, endpoint string) config.ModelConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "custom.yaml")
	content := fmt.Sprintf(`model:
  protocol: easygo
  name: custom-alias
  endpoint: %s
  apikey: "{GATEWAY_BEARER}"
  streaming: false
  timeout: 2s
database:
  driver: memory
memory:
  enabled: false
`, endpoint)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path, func(k string) (string, bool) { return "gateway-bearer", k == "GATEWAY_BEARER" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Model
}

func customRemoteServer(t *testing.T, providerURL string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	g, err := gateway.New(gateway.Config{Models: map[string]gateway.Model{"custom-alias": {Protocol: "custom", Endpoint: providerURL, Model: "vendor-id", Custom: &gateway.CustomMapping{
		Request:  map[string]string{"model": "settings.model", "messages": "payload.messages", "tools": "payload.tools", "tool_choice": "settings.choice"},
		Response: map[string]string{"content": "result.content", "finish_reason": "result.finish", "usage.input_tokens": "stats.input", "usage.output_tokens": "stats.output"},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := gateway.NewHandler(g, gateway.HandlerConfig{BearerToken: "gateway-bearer"})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var wire gateway.GenerateRequest
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Error(err)
		}
		if wire.Stream || wire.Model != "custom-alias" || r.URL.Path != "/v1/generate" {
			t.Errorf("wrong custom request mode: %+v", wire)
		}
		handler.ServeHTTP(w, r)
	}))
}

func TestRemoteCustomAliasRunsShippedRuntime(t *testing.T) {
	var providerCalls, remoteCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Settings struct {
				Model string `json:"model"`
			} `json:"settings"`
			Payload struct {
				Messages []ai.Message `json:"messages"`
			} `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Settings.Model != "vendor-id" {
			t.Error("custom model mapping not applied")
		}
		w.Header().Set("Content-Type", "application/json")
		if providerCalls.Add(1) == 1 {
			fmt.Fprint(w, `{"result":{"content":[{"type":"tool_call","id":"calc","name":"calculator","arguments":{"operation":"add","a":2,"b":3}}],"finish":"tool_calls"},"stats":{"input":10,"output":2}}`)
		} else {
			last := request.Payload.Messages[len(request.Payload.Messages)-1]
			if last.Role != "tool" || last.Content[0].ID != "calc" || !strings.Contains(last.Content[0].Text, "5") {
				t.Errorf("lost calculator result: %+v", last)
			}
			fmt.Fprint(w, `{"result":{"content":[{"type":"text","text":"5"}],"finish":"stop"},"stats":{"input":20,"output":3}}`)
		}
	}))
	defer provider.Close()
	remote := customRemoteServer(t, provider.URL, &remoteCalls)
	defer remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m, err := chatmodel.New(ctx, customRemoteModel(t, remote.URL+"/v1/generate"))
	if err != nil {
		t.Fatal(err)
	}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: m, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 3, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	if remoteCalls.Load() != 0 {
		t.Fatal("startup performed discovery")
	}
	store := conversation.NewMemory()
	session, _ := store.Create(ctx, "alice")
	run, err := agentruntime.ClaimQueuedRun(ctx, store, agent, "alice", session.ID, "2+3?")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	terminal, err := agentruntime.Consume(run, nil)
	if err != nil || terminal.Kind != agentruntime.EventCompleted || terminal.Text != "5" {
		t.Fatalf("custom runtime failed: %+v %v", terminal, err)
	}
	if remoteCalls.Load() != 2 || providerCalls.Load() != 2 {
		t.Fatalf("unexpected retry or missing turn: remote=%d provider=%d", remoteCalls.Load(), providerCalls.Load())
	}
	history, _ := store.History(ctx, "alice", session.ID, 0, 10)
	if len(history) != 1 || len(history[0].Messages) != 4 || history[0].Messages[3].ResponseMeta.TokenUsage.TotalTokens != 23 {
		t.Fatal("custom response metadata/transcript lost")
	}
}

func TestRemoteCustomCompletedModePreservesFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			started, finished, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var remoteCalls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				if mode == "error" {
					http.Error(w, "fixture failure", 500)
					return
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer provider.Close()
			defer close(release)
			remote := customRemoteServer(t, provider.URL, &remoteCalls)
			defer remote.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			m, err := chatmodel.New(ctx, customRemoteModel(t, remote.URL+"/v1/generate"))
			if err != nil {
				t.Fatal(err)
			}
			agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: m, Agent: config.AgentConfig{MaxSteps: 2, ContextTokens: 24000}})
			if err != nil {
				t.Fatal(err)
			}
			store := conversation.NewMemory()
			session, _ := store.Create(ctx, "alice")
			run, err := agentruntime.ClaimQueuedRun(ctx, store, agent, "alice", session.ID, "go")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan agentruntime.Event, 1)
			go func() { event, _ := agentruntime.Consume(run, nil); done <- event }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("custom provider not reached")
			}
			expected := agentruntime.EventFailed
			if mode == "cancel" {
				expected = agentruntime.EventCanceled
				run.Cancel()
			}
			select {
			case terminal := <-done:
				if terminal.Kind != expected {
					t.Fatalf("wrong terminal: %+v", terminal)
				}
			case <-ctx.Done():
				t.Fatal("run did not terminate")
			}
			run.Close()
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("upstream not canceled")
			}
			if remoteCalls.Load() != 1 {
				t.Fatal("completed mode retried request")
			}
		})
	}
}
