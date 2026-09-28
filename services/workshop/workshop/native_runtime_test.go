//go:build linux

package workshop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These opt-in tests execute the production runner with real native binaries.
// Each selected prerequisite is mandatory: an invalid binary or denied loopback
// listener fails, rather than silently converting an attempted proof to a skip.
func TestNativeRuntimeDirectAndResume(t *testing.T) {
	for _, tc := range []struct{ engine, protocol, variable string }{
		{"codex", "responses", "WORKSHOP_NATIVE_CODEX"},
		{"claude", "anthropic", "WORKSHOP_NATIVE_CLAUDE"},
		{"pi", "chat_completions", "WORKSHOP_NATIVE_PI"},
		{"openclaw", "chat_completions", "WORKSHOP_NATIVE_OPENCLAW"},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			binary := os.Getenv(tc.variable)
			if binary == "" {
				t.Skipf("external native prerequisite: set %s to an absolute binary path", tc.variable)
			}
			if !filepath.IsAbs(binary) {
				t.Fatalf("%s must be an absolute path", tc.variable)
			}
			probeHome := t.TempDir()
			for _, dir := range []string{"codex", "claude", "pi", "openclaw"} {
				if err := os.Mkdir(filepath.Join(probeHome, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "--version")
			cmd.Dir = probeHome
			cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + probeHome, "CODEX_HOME=" + filepath.Join(probeHome, "codex"), "CLAUDE_CONFIG_DIR=" + filepath.Join(probeHome, "claude"), "PI_CODING_AGENT_DIR=" + filepath.Join(probeHome, "pi"), "PI_OFFLINE=1", "OPENCLAW_STATE_DIR=" + filepath.Join(probeHome, "openclaw"), "OPENCLAW_CONFIG_PATH=" + filepath.Join(probeHome, "openclaw", "openclaw.json")}
			version, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("isolated version probe: %v: %s", err, version)
			}
			t.Logf("binary=%s version=%s", binary, strings.TrimSpace(string(version)))

			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("native fixture prerequisite: loopback listen: %v", err)
			}
			var mu sync.Mutex
			var bodies []string
			var requestErrors []string
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Claude probes endpoint liveness separately from authenticated generation.
				if r.Method == "HEAD" && r.URL.Path == "/api/hello" {
					w.WriteHeader(204)
					return
				}
				raw, readErr := io.ReadAll(io.LimitReader(r.Body, 4<<20))
				var body map[string]any
				decodeErr := json.Unmarshal(raw, &body)
				mu.Lock()
				bodies = append(bodies, string(raw))
				if readErr != nil || decodeErr != nil || r.Method != "POST" || body["model"] != "fixture-model" {
					requestErrors = append(requestErrors, fmt.Sprintf("unexpected request method=%s path=%s model=%v read=%v decode=%v", r.Method, r.URL.Path, body["model"], readErr, decodeErr))
				}
				if r.Header.Get("Authorization") != "Bearer native-fixture-key" && r.Header.Get("x-api-key") != "native-fixture-key" {
					requestErrors = append(requestErrors, "missing selected fixture credential")
				}
				mu.Unlock()
				answer := "native-fixture-first"
				if strings.Contains(string(raw), "resume-probe") {
					answer = "native-fixture-resumed"
				}
				writeNativeFixture(w, r.URL.Path, answer)
			}))
			server.Listener = ln
			server.Start()
			defer server.Close()
			t.Setenv("WORKSHOP_NATIVE_FIXTURE_KEY", "native-fixture-key")
			runner, err := NewCommandRunner(map[string]EngineConfig{tc.engine: {Binary: binary}}, 4<<20)
			if err != nil {
				t.Fatal(err)
			}
			profile := RuntimeProfile{Engine: tc.engine, Protocol: tc.protocol, Model: "fixture-model", BaseURL: server.URL + "/v1", APIKeyEnv: "WORKSHOP_NATIVE_FIXTURE_KEY"}
			in := Invocation{Namespace: "native-fixture", Workspace: t.TempDir(), Workflow: Workflow{Name: "native-fixture", Version: "1", Instructions: "Return the requested text. Do not use tools.", Engine: tc.engine, Model: profile.Model, Policy: "read-only", TimeoutSeconds: 60, RuntimeSpec: &profile}, Input: "first-probe"}
			for _, turn := range []string{"first", "resume"} {
				if turn == "resume" {
					in.Input = "resume-probe"
				}
				ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
				result, runErr := runner.Run(ctx, in, func(e Event) error {
					if e.Kind == "diagnostic" {
						t.Logf("%s diagnostic: %s", turn, e.Text)
					}
					return nil
				})
				stop()
				if runErr != nil {
					t.Fatalf("%s real runner failed: %v; result=%+v", turn, runErr, result)
				}
				if _, err := uuid.Parse(result.SessionID); err != nil {
					t.Fatalf("%s invalid session: %q", turn, result.SessionID)
				}
				want := "native-fixture-first"
				if turn == "resume" {
					want = "native-fixture-resumed"
					if result.SessionID != in.SessionID {
						t.Fatalf("resume changed session %s -> %s", in.SessionID, result.SessionID)
					}
				}
				if result.Text != want || result.Usage.OutputTokens <= 0 {
					t.Fatalf("%s result text/usage mismatch: %+v", turn, result)
				}
				in.SessionID = result.SessionID
				t.Logf("%s session=%s text=%s usage=%+v", turn, result.SessionID, result.Text, result.Usage)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requestErrors) != 0 {
				t.Fatalf("provider request errors: %v", requestErrors)
			}
			if len(bodies) < 2 || !strings.Contains(bodies[len(bodies)-1], "native-fixture-first") {
				t.Fatal("resume request did not retain the first assistant response")
			}
		})
	}
}

func writeNativeFixture(w http.ResponseWriter, path, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(name string, data any) {
		if name != "" {
			fmt.Fprintf(w, "event: %s\n", name)
		}
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	if strings.HasSuffix(path, "/responses") {
		item := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
		response := map[string]any{"id": "resp_fixture", "object": "response", "created_at": 1, "status": "completed", "model": "fixture-model", "output": []any{item}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 2, "total_tokens": 5, "input_tokens_details": map[string]any{"cached_tokens": 0}}}
		send("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_fixture", "status": "in_progress", "output": []any{}}})
		send("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}})
		send("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_fixture", "output_index": 0, "content_index": 0, "delta": text})
		send("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		send("response.completed", map[string]any{"type": "response.completed", "response": response})
	} else if strings.HasSuffix(path, "/messages") {
		send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "fixture-model", "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 3, "output_tokens": 0}}})
		send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 2}})
		send("message_stop", map[string]any{"type": "message_stop"})
	} else if strings.HasSuffix(path, "/chat/completions") {
		for _, choice := range []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}} {
			send("", map[string]any{"id": "chatcmpl-fixture", "object": "chat.completion.chunk", "created": 1, "model": "fixture-model", "choices": []any{choice}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	} else {
		w.WriteHeader(http.StatusNotFound)
	}
}

// These parser contract tests require neither native binaries nor a listener.
// They are not evidence of a successful native CLI run.
func TestNativeOpenClawRejectsFailureOutcomes(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"top_level_error", `{"error":{"message":"failed"},"payloads":[],"meta":{"agentMeta":{"sessionId":"11111111-2222-4333-8444-555555555555"}}}`},
		{"aborted", `{"payloads":[],"meta":{"aborted":true,"agentMeta":{"sessionId":"11111111-2222-4333-8444-555555555555"}}}`},
		{"meta_error", `{"payloads":[],"meta":{"error":{"kind":"retry_limit","message":"failed"},"agentMeta":{"sessionId":"11111111-2222-4333-8444-555555555555"}}}`},
		{"error_payload", `{"payloads":[{"text":"model failed","isError":true}],"meta":{"agentMeta":{"sessionId":"11111111-2222-4333-8444-555555555555"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &streamParser{engine: "openclaw", redact: func(s string) string { return s }, emit: func(Event) error { return nil }, cancel: func() {}, limit: 4096}
			p.Write([]byte(tc.raw))
			if err := p.finish(); err == nil {
				t.Fatalf("accepted failed OpenClaw outcome: success=%v result=%+v", p.success, p.result)
			}
		})
	}
}

func TestNativePiRequiresSettled(t *testing.T) {
	p := &streamParser{engine: "pi", redact: func(s string) string { return s }, emit: func(Event) error { return nil }, cancel: func() {}, limit: 4096}
	p.Write([]byte("{\"type\":\"session\",\"id\":\"11111111-2222-4333-8444-555555555555\"}\n{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"stopReason\":\"stop\",\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\n{\"type\":\"agent_end\",\"willRetry\":false}\n"))
	if p.success {
		t.Fatal("agent_end must not settle Pi")
	}
	p.Write([]byte("{\"type\":\"agent_settled\"}\n"))
	if err := p.finish(); err != nil || !p.success {
		t.Fatalf("settled turn failed: %v", err)
	}
}
