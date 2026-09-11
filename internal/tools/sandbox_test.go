package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "easygo-agent/internal/agent/runtime"

	"github.com/cloudwego/eino/components/tool"
)

const testSandboxControllerToken = "test-sandbox-controller-token-0123456789abcdef"

type observedSandboxRequest struct {
	method, path, sessionID, runID string
	body                           map[string]any
}

func TestSandboxToolsCallControllerContract(t *testing.T) {
	var mu sync.Mutex
	var observed []observedSandboxRequest
	application := `{"id":"app-1","state":"ready","created_at":"2026-09-11T00:00:00Z","hard_expires_at":"2026-09-11T05:00:00Z","idle_ttl_seconds":600,"distinct_runs":1,"workspace_bytes":42,"workspace_preserved":true}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSandboxControllerToken {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		mu.Lock()
		observed = append(observed, observedSandboxRequest{method: r.Method, path: r.URL.Path, sessionID: r.Header.Get("X-EasyGo-Session-ID"), runID: r.Header.Get("X-EasyGo-Run-ID"), body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/applications":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"application":%s,"created":true}`, application)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/applications/app-1/exec":
			fmt.Fprintf(w, `{"application":%s,"exit_code":0,"stdout":"ok\n","stderr":"","truncated":false,"duration_ms":7}`, application)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/applications/app-1/files/read":
			fmt.Fprintf(w, `{"application":%s,"path":"/workspace/main.go","content":"package main","size_bytes":12,"next_offset":12,"eof":true}`, application)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/applications/app-1/files/write":
			fmt.Fprintf(w, `{"application":%s,"path":"/workspace/main.go","size_bytes":12}`, application)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/applications/app-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			fmt.Fprintf(w, `{"application":%s}`, application)
		}
	}))
	defer server.Close()

	toolsByName := sandboxToolsByName(t, server.URL)
	wantNames := []string{"sandbox_apply", "sandbox_create", "sandbox_exec", "sandbox_write_file", "sandbox_read_file", "sandbox_status", "sandbox_release", "sandbox_destroy"}
	for _, name := range wantNames {
		if toolsByName[name] == nil {
			t.Fatalf("tool %q is missing", name)
		}
	}
	ctx := agentruntime.WithInvocationIdentity(context.Background(), agentruntime.InvocationIdentity{SessionID: "session-1", RunID: "run-1"})
	invocations := []struct {
		name, arguments string
	}{
		{"sandbox_apply", `{}`},
		{"sandbox_create", `{"application_id":"app-1"}`},
		{"sandbox_exec", `{"application_id":"app-1","command":"go run .","cwd":"/workspace","stdin":"input","timeout_seconds":5,"session_id":"attacker","run_id":"attacker"}`},
		{"sandbox_write_file", `{"application_id":"app-1","path":"/workspace/main.go","content":"package main","executable":true}`},
		{"sandbox_read_file", `{"application_id":"app-1","path":"/workspace/main.go"}`},
		{"sandbox_status", `{"application_id":"app-1"}`},
		{"sandbox_release", `{"application_id":"app-1"}`},
		{"sandbox_destroy", `{"application_id":"app-1"}`},
	}
	for _, invocation := range invocations {
		result, err := toolsByName[invocation.name].InvokableRun(ctx, invocation.arguments)
		if err != nil {
			t.Fatalf("%s: %v", invocation.name, err)
		}
		if invocation.name == "sandbox_destroy" && !strings.Contains(result, `"destroyed":true`) {
			t.Fatalf("destroy result=%s", result)
		}
		if invocation.name == "sandbox_apply" && !strings.Contains(result, `"application_id":"app-1"`) {
			t.Fatalf("apply result does not expose application_id: %s", result)
		}
	}

	mu.Lock()
	requests := append([]observedSandboxRequest(nil), observed...)
	mu.Unlock()
	want := []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/applications"},
		{http.MethodPost, "/v1/applications/app-1/create"},
		{http.MethodPost, "/v1/applications/app-1/exec"},
		{http.MethodPost, "/v1/applications/app-1/files/write"},
		{http.MethodPost, "/v1/applications/app-1/files/read"},
		{http.MethodGet, "/v1/applications/app-1"},
		{http.MethodPost, "/v1/applications/app-1/release"},
		{http.MethodDelete, "/v1/applications/app-1"},
	}
	if len(requests) != len(want) {
		t.Fatalf("requests=%+v", requests)
	}
	for i, expected := range want {
		got := requests[i]
		if got.method != expected.method || got.path != expected.path || got.sessionID != "session-1" {
			t.Errorf("request[%d]=%+v", i, got)
		}
		if got.runID != "run-1" {
			t.Errorf("request[%d] run ID=%q", i, got.runID)
		}
	}
	if requests[2].body["stdin"] != "input" || requests[2].body["timeout_seconds"] != float64(5) {
		t.Fatalf("exec body=%v", requests[2].body)
	}
	if requests[4].body["max_bytes"] != float64(4096) {
		t.Fatalf("read body=%v", requests[4].body)
	}
}

func TestAgentToolIncludesCompleteSandboxSurface(t *testing.T) {
	items, err := NewAgentTool(SandboxControllerConfig{
		BaseURL:        "http://127.0.0.1:8787",
		AuthToken:      testSandboxControllerToken,
		RequestTimeout: time.Second,
		MaxOutputBytes: 4096,
	}).AllTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"calculator":         true,
		"sandbox_apply":      true,
		"sandbox_create":     true,
		"sandbox_exec":       true,
		"sandbox_write_file": true,
		"sandbox_read_file":  true,
		"sandbox_status":     true,
		"sandbox_release":    true,
		"sandbox_destroy":    true,
	}
	for _, item := range items {
		info, infoErr := item.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		delete(want, info.Name)
	}
	if len(want) != 0 || len(items) != 9 {
		t.Fatalf("missing tools=%v count=%d", want, len(items))
	}
}

func TestSandboxToolFailsClosedWithoutInvocationIdentity(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	tool := sandboxToolsByName(t, server.URL)["sandbox_apply"]
	_, err := tool.InvokableRun(context.Background(), `{}`)
	if err == nil || !strings.Contains(err.Error(), "trusted session and run identity") {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("controller received %d calls", calls.Load())
	}
}

func TestSandboxToolReportsStructuredControllerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"capacity_exhausted","message":"all sandboxes are busy"}}`))
	}))
	defer server.Close()
	ctx := agentruntime.WithInvocationIdentity(context.Background(), agentruntime.InvocationIdentity{SessionID: "session-1", RunID: "run-1"})
	_, err := sandboxToolsByName(t, server.URL)["sandbox_apply"].InvokableRun(ctx, `{}`)
	if err == nil || !strings.Contains(err.Error(), "capacity_exhausted: all sandboxes are busy (retry after 3)") {
		t.Fatalf("error=%v", err)
	}
}

func TestSandboxToolRejectsUnsafeInputBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	ctx := agentruntime.WithInvocationIdentity(context.Background(), agentruntime.InvocationIdentity{SessionID: "session-1", RunID: "run-1"})
	toolsByName := sandboxToolsByName(t, server.URL)
	for _, tc := range []struct{ name, arguments string }{
		{"sandbox_exec", `{"application_id":"../bad","command":"true"}`},
		{"sandbox_exec", `{"application_id":"app-1","command":"true","cwd":"/etc"}`},
		{"sandbox_exec", `{"application_id":"app-1","command":"true","cwd":"/workspace/../etc"}`},
		{"sandbox_exec", `{"application_id":"app-1","command":"true","timeout_seconds":601}`},
		{"sandbox_write_file", `{"application_id":"app-1","path":"/etc/passwd","content":"x"}`},
		{"sandbox_read_file", `{"application_id":"app-1","path":"/workspace/x","offset":-1}`},
	} {
		if _, err := toolsByName[tc.name].InvokableRun(ctx, tc.arguments); err == nil {
			t.Errorf("%s accepted unsafe input", tc.name)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("controller received %d calls", calls.Load())
	}
}

func TestNewSandboxControllerClientValidation(t *testing.T) {
	base := SandboxControllerConfig{BaseURL: "http://127.0.0.1:8787", AuthToken: testSandboxControllerToken, RequestTimeout: time.Second, MaxOutputBytes: 1024}
	for _, mutate := range []func(*SandboxControllerConfig){
		func(c *SandboxControllerConfig) { c.BaseURL = "ftp://127.0.0.1" },
		func(c *SandboxControllerConfig) { c.BaseURL = "http://controller.example.com:8787" },
		func(c *SandboxControllerConfig) { c.AuthToken = "short" },
		func(c *SandboxControllerConfig) { c.RequestTimeout = 0 },
		func(c *SandboxControllerConfig) { c.MaxOutputBytes = 1024*1024 + 1 },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := newSandboxControllerClient(cfg); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
}

func TestSandboxControllerClientNeverFollowsRedirects(t *testing.T) {
	var redirectedCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedCalls.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Redirect(response, &http.Request{}, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client, err := newSandboxControllerClient(SandboxControllerConfig{
		BaseURL:        source.URL,
		AuthToken:      testSandboxControllerToken,
		RequestTimeout: time.Second,
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentruntime.WithInvocationIdentity(context.Background(), agentruntime.InvocationIdentity{SessionID: "session-1", RunID: "run-1"})
	if _, err := client.apply(ctx, SandboxApplyInput{}); err == nil {
		t.Fatal("redirect response was accepted")
	}
	if redirectedCalls.Load() != 0 {
		t.Fatalf("trusted headers were forwarded across %d redirected requests", redirectedCalls.Load())
	}
}

func sandboxToolsByName(t *testing.T, baseURL string) map[string]tool.InvokableTool {
	t.Helper()
	items, err := NewSandboxTools(SandboxControllerConfig{BaseURL: baseURL, AuthToken: testSandboxControllerToken, RequestTimeout: time.Second, MaxOutputBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]tool.InvokableTool, len(items))
	for _, item := range items {
		info, err := item.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		invokable, ok := item.(tool.InvokableTool)
		if !ok {
			t.Fatalf("tool %q is not invokable", info.Name)
		}
		result[info.Name] = invokable
	}
	return result
}
