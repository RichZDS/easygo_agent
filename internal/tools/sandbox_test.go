package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/sandbox"

	"github.com/cloudwego/eino/components/tool"
)

const testSandboxControllerToken = "test-sandbox-controller-token-0123456789abcdef"

func TestSandboxToolsCallControllerContract(t *testing.T) {
	handler, err := sandbox.NewInProcessHandler(testSandboxControllerToken)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	toolsByName := sandboxToolsByName(t, server.URL)
	wantNames := []string{"sandbox_apply", "sandbox_create", "sandbox_exec", "sandbox_write_file", "sandbox_read_file", "sandbox_status", "sandbox_release", "sandbox_destroy"}
	for _, name := range wantNames {
		if toolsByName[name] == nil {
			t.Fatalf("tool %q is missing", name)
		}
	}
	ctx := agentruntime.WithInvocationIdentity(context.Background(), agentruntime.InvocationIdentity{SessionID: "session-1", RunID: "run-1"})
	applyResult, err := toolsByName["sandbox_apply"].InvokableRun(ctx, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var applied struct {
		ApplicationID string `json:"application_id"`
	}
	if err := json.Unmarshal([]byte(applyResult), &applied); err != nil || applied.ApplicationID == "" {
		t.Fatalf("apply result=%s err=%v", applyResult, err)
	}
	idJSON, _ := json.Marshal(applied.ApplicationID)
	invocations := []struct {
		name, arguments string
	}{
		{"sandbox_create", `{"application_id":` + string(idJSON) + `}`},
		{"sandbox_exec", `{"application_id":` + string(idJSON) + `,"command":"true","cwd":"/workspace","stdin":"input","timeout_seconds":5,"session_id":"attacker","run_id":"attacker"}`},
		{"sandbox_write_file", `{"application_id":` + string(idJSON) + `,"path":"/workspace/main.go","content":"package main","executable":true}`},
		{"sandbox_read_file", `{"application_id":` + string(idJSON) + `,"path":"/workspace/main.go"}`},
		{"sandbox_status", `{"application_id":` + string(idJSON) + `}`},
		{"sandbox_release", `{"application_id":` + string(idJSON) + `}`},
		{"sandbox_destroy", `{"application_id":` + string(idJSON) + `}`},
	}
	for _, invocation := range invocations {
		result, runErr := toolsByName[invocation.name].InvokableRun(ctx, invocation.arguments)
		if runErr != nil {
			t.Fatalf("%s: %v", invocation.name, runErr)
		}
		if invocation.name == "sandbox_destroy" && !strings.Contains(result, `"destroyed":true`) {
			t.Fatalf("destroy result=%s", result)
		}
	}
	cross := agentruntime.WithInvocationIdentity(context.Background(), agentruntime.InvocationIdentity{SessionID: "session-other", RunID: "run-other"})
	if _, err := toolsByName["sandbox_apply"].InvokableRun(context.Background(), `{}`); err == nil || !strings.Contains(err.Error(), "trusted session and run identity") {
		t.Fatalf("missing identity error=%v", err)
	}
	if _, err := toolsByName["sandbox_status"].InvokableRun(cross, `{"application_id":`+string(idJSON)+`}`); err == nil {
		t.Fatal("cross-session status succeeded; identity headers did not reach the handler")
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
