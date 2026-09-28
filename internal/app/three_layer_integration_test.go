package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/services/workshop/workshop"
)

// This crosses the production application wiring, native loop, real HTTP model
// gateway transport, session-scoped workshop HTTP tools, and a real OS child.
// The provider and CLI output are deterministic fixtures, not live LLM calls.
func TestThreeLayersCompleteWorkshopArtifact(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("requires local Python child fixture")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "cli-fixture")
	code := `#!/usr/bin/python3
import json, pathlib, sys
prompt = sys.stdin.read()
if 'write proof' not in prompt:
    sys.exit(2)
print(json.dumps({'type':'thread.started','thread_id':'11111111-2222-4333-8444-555555555555'}), flush=True)
pathlib.Path('report.txt').write_text('verified artifact')
print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':'verified artifact'}}), flush=True)
print(json.dumps({'type':'turn.completed','usage':{'input_tokens':12,'output_tokens':3}}), flush=True)
`
	if err := os.WriteFile(binary, []byte(code), 0700); err != nil {
		t.Fatal(err)
	}
	service, err := workshop.New(workshop.Config{
		Root: filepath.Join(root, "workspaces"), Concurrency: 1, QueueCapacity: 2,
		Engines:   map[string]workshop.EngineConfig{"codex": {Binary: binary}},
		Workflows: []workshop.Workflow{{Name: "proof", Version: "v1", Engine: "codex", Model: "fixture", Policy: "workspace-write", TimeoutSeconds: 5, Instructions: "Produce the requested proof file.", Artifacts: []string{"report.txt"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ws := httptest.NewServer(workshop.Handler(service, "workshop-fixture-key"))
	defer ws.Close()
	var modelCalls atomic.Int32
	var sawArtifact atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer model-fixture-key" {
			t.Error("model authorization missing")
		}
		var input struct {
			Messages []map[string]any `json:"messages"`
			Stream   bool             `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if !input.Stream {
			t.Error("application unexpectedly bypassed stream path")
		}
		var lastTool map[string]any
		for _, message := range input.Messages {
			if message["role"] == "tool" {
				lastTool = message
			}
		}
		name, arguments := "workshop_submit", `{"workflow":"proof","input":"write proof","idempotency_key":"proof-once"}`
		final := false
		if lastTool == nil {
			name, arguments = "workshop_catalog", `{}`
		} else {
			text, _ := lastTool["content"].(string)
			if strings.HasPrefix(text, "[") {
				var catalog []struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal([]byte(text), &catalog); err != nil || len(catalog) != 1 || catalog[0].Name != "proof" {
					t.Error("workflow catalog did not reach model")
					http.Error(w, "catalog missing", 500)
					return
				}
			} else {
				var task struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				}
				if err := json.Unmarshal([]byte(text), &task); err != nil || task.ID == "" {
					t.Errorf("tool result is not a workshop task: %s", text)
					http.Error(w, "tool result missing", 500)
					return
				}
				if task.Status == "succeeded" {
					final = true
					sawArtifact.Store(strings.Contains(text, "report.txt") && strings.Contains(text, "verified artifact") && strings.Contains(text, "sha256"))
				} else {
					name = "workshop_get"
					raw, _ := json.Marshal(map[string]string{"task_id": task.ID})
					arguments = string(raw)
					time.Sleep(5 * time.Millisecond)
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) { raw, _ := json.Marshal(v); _, _ = fmt.Fprintf(w, "data: %s\n\n", raw) }
		delta := map[string]any{"role": "assistant"}
		reason := "tool_calls"
		if final {
			delta["content"], reason = "Workshop proof: verified artifact", "stop"
		} else {
			delta["tool_calls"] = []map[string]any{{"index": 0, "id": fmt.Sprintf("call-%d", modelCalls.Load()), "type": "function", "function": map[string]string{"name": name, "arguments": arguments}}}
		}
		send(map[string]any{"id": "fixture-response", "choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}}})
		send(map[string]any{"id": "fixture-response", "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": reason}}})
		send(map[string]any{"choices": []any{}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 4, "total_tokens": 24}})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	t.Setenv("MODEL_API_KEY", "model-fixture-key")
	t.Setenv("WORKSHOP_TOKEN", "workshop-fixture-key")
	configPath := filepath.Join(root, "config.yaml")
	config := fmt.Sprintf("agent:\n  max_steps: 64\n  context_tokens: 500000\nmodel:\n  name: fixture\n  endpoint: %s\n  apikey: '{MODEL_API_KEY}'\ndatabase:\n  driver: memory\nmemory:\n  enabled: false\nworkshop:\n  enabled: true\n  base_url: %s\n  auth_token: '{WORKSHOP_TOKEN}'\n", provider.URL, ws.URL)
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a, err := assemble(ctx, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if err := a.buildAgent(ctx); err != nil {
		t.Fatal(err)
	}
	session, err := a.store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	run, err := agentruntime.ClaimQueuedRun(ctx, a.store, a.agent, "alice", session.ID, "Make a proof through the CLI workshop")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	for {
		e := run.Next()
		if e.Kind == agentruntime.EventFailed || e.Kind == agentruntime.EventCanceled {
			t.Fatalf("run failed: %+v", e)
		}
		if e.Kind == agentruntime.EventCompleted {
			if !strings.Contains(e.Text, "verified artifact") {
				t.Fatalf("missing final answer: %s", e.Text)
			}
			break
		}
	}
	if modelCalls.Load() < 3 || !sawArtifact.Load() {
		t.Fatal("model did not consume verified workshop artifact")
	}
	history, err := a.store.History(ctx, "alice", session.ID, 0, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("final history was not committed: %v, rows=%d", err, len(history))
	}
}
