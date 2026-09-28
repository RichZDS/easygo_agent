//go:build linux

package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"easygo-agent/rpc"
	"github.com/google/uuid"
)

// The real image is deliberately separate from the adversarial fake CLI image.
// No provider keys or paid endpoints: native CLIs consume deterministic local SSE.
func TestDockerNativeRuntimes(t *testing.T) {
	endpoint, image := os.Getenv("EASYGO_DOCKER_TEST_ENDPOINT"), os.Getenv("EASYGO_DOCKER_NATIVE_IMAGE")
	if endpoint == "" || image == "" {
		t.Skip("set explicit dedicated Docker endpoint and EASYGO_DOCKER_NATIVE_IMAGE")
	}
	for _, tc := range []struct{ engine, protocol, path string }{{"codex", "responses", "/v1/responses"}, {"claude", "anthropic", "/v1/messages"}, {"pi", "chat_completions", "/v1/chat/completions"}, {"openclaw", "chat_completions", "/v1/chat/completions"}} {
		t.Run(tc.engine, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "native-proof-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			var mu sync.Mutex
			var bodies []string
			phase := "text"
			toolRequests := 0
			cancellationEntered := make(chan struct{})
			var cancellationOnce sync.Once
			gateway := nativeRelayFixture(t, func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
				var p struct {
					Namespace, Model, Protocol string
					Body                       json.RawMessage
				}
				if json.Unmarshal(raw, &p) != nil || p.Namespace != "task-namespace" || p.Model != "fixture-model" || p.Protocol != tc.protocol {
					t.Errorf("invalid relay scope: %s", raw)
					return nil, rpc.InvalidParams()
				}
				mu.Lock()
				bodies = append(bodies, string(p.Body))
				currentPhase := phase
				issueTool := false
				if currentPhase == "tools" || currentPhase == "readonly" {
					toolRequests++
					issueTool = toolRequests == 1
				}
				mu.Unlock()
				if currentPhase == "cancel" {
					cancellationOnce.Do(func() { close(cancellationEntered) })
					<-ctx.Done()
					return nil, rpc.Failure(-32000, "fixture cancellation")
				}
				if issueTool {
					rec := httptest.NewRecorder()
					if e := writeNativeToolFixture(rec, tc.engine, tc.path, p.Body); e != nil {
						t.Error(e)
						return nil, rpc.Failure(-32000, "tool fixture unsupported")
					}
					return map[string]any{"content_type": "text/event-stream", "body": rec.Body.Bytes()}, nil
				}
				answer := "native-fixture-first"
				if currentPhase == "tools" || currentPhase == "readonly" {
					answer = "native-tool-done"
				}
				if currentPhase == "text" && strings.Contains(string(p.Body), "resume-probe") {
					answer = "native-fixture-resumed"
				}
				rec := httptest.NewRecorder()
				writeNativeFixture(rec, tc.path, answer)
				return map[string]any{"content_type": rec.Header().Get("Content-Type"), "body": rec.Body.Bytes()}, nil
			})
			cfg := Config{Root: root, ModelGateway: gateway, Sandbox: SandboxConfig{Mode: "docker", DockerBinary: os.Getenv("EASYGO_DOCKER_TEST_BINARY"), Endpoint: endpoint, Image: image, Owner: "native-" + uuid.NewString(), HostRoot: root, DiskQuotaBytes: 64 << 20, DiskQuotaFiles: 10000, DiskPollMS: 200}}
			r, err := NewDockerRunner(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err = r.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(root, "workspaces", uuid.NewString())
			if err = os.MkdirAll(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			// Probe the installed version inside the same constrained runtime image.
			name := "easygo-" + uuid.NewString()
			opts := r.containerOptions(name, workspace, "")
			opts = append(opts, "--env", "EASYGO_RELAY_SOCKET="+runtimeRelay+"/model.sock", "--env", "HOME=/workspace", "--env", "CODEX_HOME=/workspace/codex", "--env", "CLAUDE_CONFIG_DIR=/workspace/claude", "--env", "PI_CODING_AGENT_DIR=/workspace/pi", "--env", "OPENCLAW_STATE_DIR=/workspace/openclaw", r.cfg.Image, tc.engine, "--version")
			if _, err = r.output(ctx, opts...); err != nil {
				t.Fatal(err)
			}
			version, versionErr := r.output(ctx, "start", "--attach", name)
			cleanupErr := r.cleanup(name)
			if versionErr != nil || cleanupErr != nil {
				t.Fatalf("version: %v cleanup: %v", versionErr, cleanupErr)
			}
			t.Logf("image=%s %s version=%s", r.cfg.Image, tc.engine, version)
			profile := RuntimeProfile{Engine: tc.engine, Protocol: tc.protocol, GatewayModel: "fixture-model"}
			in := Invocation{Namespace: "task-namespace", Workspace: workspace, Workflow: Workflow{Name: "native-proof", Version: "1", Engine: tc.engine, Model: "fixture-model", Policy: "read-only", Instructions: "Return requested text. Do not use tools.", TimeoutSeconds: 60, RuntimeSpec: &profile}, Input: "first-probe"}
			for _, turn := range []string{"first", "resume"} {
				if turn == "resume" {
					in.Input = "resume-probe"
				}
				runCtx, stop := context.WithTimeout(ctx, 60*time.Second)
				result, e := r.Run(runCtx, in, func(event Event) error {
					if event.Kind == "diagnostic" {
						t.Log(turn + " diagnostic: " + event.Text)
					}
					return nil
				})
				stop()
				if e != nil {
					t.Fatalf("%s: %v; result=%+v", turn, e, result)
				}
				if _, e = uuid.Parse(result.SessionID); e != nil {
					t.Fatal("missing native session")
				}
				want := "native-fixture-first"
				if turn == "resume" {
					want = "native-fixture-resumed"
					if result.SessionID != in.SessionID {
						t.Fatal("resume session changed")
					}
				}
				if result.Text != want || result.Usage.OutputTokens <= 0 {
					t.Fatalf("result mismatch %+v", result)
				}
				in.SessionID = result.SessionID
				t.Logf("%s native session=%s text=%s usage=%+v", turn, result.SessionID, result.Text, result.Usage)
			}
			mu.Lock()
			retained := len(bodies) >= 2 && strings.Contains(bodies[len(bodies)-1], "native-fixture-first")
			count := len(bodies)
			phase = "tools"
			mu.Unlock()
			if !retained {
				t.Fatal("native resume lost first turn")
			}
			t.Logf("scoped native first/resume callback requests=%d", count)
			in.Workflow.Policy = "workspace-write"
			in.Workflow.Instructions = "Write /workspace/native-artifact.txt containing exactly native-artifact using the available tool, then return native-tool-done."
			in.Input = "tool-write-probe"
			in.SessionID = ""
			toolCtx, toolStop := context.WithTimeout(ctx, 60*time.Second)
			result, toolErr := r.Run(toolCtx, in, func(event Event) error {
				if event.Kind == "diagnostic" {
					t.Log("tool diagnostic: " + event.Text)
				}
				return nil
			})
			toolStop()
			artifact, artifactErr := os.ReadFile(filepath.Join(workspace, "native-artifact.txt"))
			mu.Lock()
			calls := toolRequests
			lastBody := bodies[len(bodies)-1]
			phase = "cancel"
			mu.Unlock()
			if toolErr != nil || artifactErr != nil || string(artifact) != "native-artifact" || result.Text != "native-tool-done" || calls < 2 {
				evidence := "/tmp/platform-native-tool-" + tc.engine + ".json"
				if err := os.WriteFile(evidence, []byte(lastBody), 0600); err != nil {
					t.Error(err)
				}
				t.Errorf("native tool failed: run=%v artifact=%v content=%q result=%+v callbacks=%d last-body=%s", toolErr, artifactErr, artifact, result, calls, evidence)
			} else {
				t.Logf("native tool artifact=%q session=%s callbacks=%d", artifact, result.SessionID, calls)
			}
			if tc.engine == "codex" {
				// The inner override must not defeat Workflow.Policy: exercise a real exec
				// tool against the outer read-only mount, using a fresh artifact path.
				if err := os.Remove(filepath.Join(workspace, "native-artifact.txt")); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				mu.Lock()
				phase = "readonly"
				toolRequests = 0
				mu.Unlock()
				in.Workflow.Policy = "read-only"
				readCtx, readStop := context.WithTimeout(ctx, 60*time.Second)
				_, readErr := r.Run(readCtx, in, func(event Event) error {
					if event.Kind == "diagnostic" {
						t.Log("readonly diagnostic: " + event.Text)
					}
					return nil
				})
				readStop()
				_, statErr := os.Stat(filepath.Join(workspace, "native-artifact.txt"))
				mu.Lock()
				readonlyBody := bodies[len(bodies)-1]
				readonlyCalls := toolRequests
				phase = "cancel"
				mu.Unlock()
				if readErr != nil || !os.IsNotExist(statErr) || readonlyCalls < 2 || !strings.Contains(strings.ToLower(readonlyBody), "read-only file system") {
					t.Fatalf("real Codex read-only mount not proven: run=%v stat=%v requests=%d body=%s", readErr, statErr, readonlyCalls, readonlyBody)
				}
				t.Log("native Codex exec write rejected by read-only filesystem; no artifact created")
				in.Workflow.Policy = "workspace-write"
			}
			cancelCtx, stopNative := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { _, e := r.Run(cancelCtx, in, func(Event) error { return nil }); done <- e }()
			select {
			case <-cancellationEntered:
			case e := <-done:
				stopNative()
				t.Fatalf("native cancel run exited before request: %v", e)
			case <-time.After(30 * time.Second):
				stopNative()
				t.Fatal("native cancel request never started")
			}
			stopNative()
			select {
			case e := <-done:
				if !errors.Is(e, context.Canceled) {
					t.Fatalf("cancel result %v", e)
				}
			case <-time.After(25 * time.Second):
				t.Fatal("native cancel cleanup timed out")
			}
			ids, e := r.output(ctx, "ps", "-aq", "--filter", "label="+ownerLabel+"="+r.cfg.Owner)
			if e != nil || ids != "" {
				t.Fatal("native cancel leaked container", ids, e)
			}
			t.Log("native request cancellation removed task container")

		})
	}
}

// Emit one native tool call using the advertised schema; the test verifies the
// real file artifact rather than trusting model text or tool-event declarations.
func writeNativeToolFixture(w http.ResponseWriter, engine, path string, raw json.RawMessage) error {
	var body struct {
		Tools []struct {
			Type, Name string
			Function   struct{ Name string }
		}
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	names := []string{}
	for _, tool := range body.Tools {
		name := tool.Name
		if name == "" {
			name = tool.Function.Name
		}
		names = append(names, name)
	}
	name := ""
	args := map[string]any{}
	for _, candidate := range names {
		if engine == "codex" {
			switch candidate {
			case "exec_command":
				name = candidate
				args = map[string]any{"cmd": "printf native-artifact > native-artifact.txt", "workdir": "/workspace", "yield_time_ms": 1000}
			case "shell_command":
				name = candidate
				args = map[string]any{"command": "printf native-artifact > native-artifact.txt", "workdir": "/workspace"}
			case "shell":
				name = candidate
				args = map[string]any{"command": []string{"bash", "-lc", "printf native-artifact > native-artifact.txt"}, "workdir": "/workspace"}
			}
		} else if candidate == "Edit" {
			name = candidate
			args = map[string]any{"file_path": "/workspace/native-artifact.txt", "old_string": "", "new_string": "native-artifact"}
		} else if candidate == "Write" {
			name = candidate
			args = map[string]any{"file_path": "/workspace/native-artifact.txt", "content": "native-artifact"}
		} else if candidate == "write" {
			name = candidate
			args = map[string]any{"path": "/workspace/native-artifact.txt", "content": "native-artifact"}
		}
		if name != "" {
			break
		}
	}
	if name == "" {
		return fmt.Errorf("no supported native file tool; names=%v", names)
	}
	arguments, _ := json.Marshal(args)
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(event string, data any) {
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
		}
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	if strings.HasSuffix(path, "/responses") {
		item := map[string]any{"type": "function_call", "id": "fc_fixture", "call_id": "call_fixture", "name": name, "arguments": string(arguments), "status": "completed"}
		send("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_tool", "status": "in_progress", "output": []any{}}})
		send("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_fixture", "call_id": "call_fixture", "name": name, "arguments": "", "status": "in_progress"}})
		send("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_fixture", "output_index": 0, "delta": string(arguments)})
		send("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		send("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_tool", "object": "response", "status": "completed", "model": "fixture-model", "output": []any{item}, "usage": map[string]int{"input_tokens": 3, "output_tokens": 2, "total_tokens": 5}}})
	} else if strings.HasSuffix(path, "/messages") {
		send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_tool", "type": "message", "role": "assistant", "model": "fixture-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 3, "output_tokens": 0}}})
		send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "call_fixture", "name": name, "input": map[string]any{}}})
		send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(arguments)}})
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 2}})
		send("message_stop", map[string]any{"type": "message_stop"})
	} else {
		send("", map[string]any{"id": "chatcmpl_tool", "object": "chat.completion.chunk", "created": 1, "model": "fixture-model", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_fixture", "type": "function", "function": map[string]any{"name": name, "arguments": string(arguments)}}}}, "finish_reason": nil}}})
		send("", map[string]any{"id": "chatcmpl_tool", "object": "chat.completion.chunk", "created": 1, "model": "fixture-model", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}, "usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
	return nil
}
