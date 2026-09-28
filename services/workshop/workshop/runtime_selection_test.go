package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type runtimeCapture struct{ calls chan Invocation }

func (r runtimeCapture) Run(ctx context.Context, in Invocation, emit func(Event) error) (Result, error) {
	r.calls <- in
	return Result{SessionID: "11111111-2222-4333-8444-555555555555", Text: "done"}, nil
}
func runtimeDone(t *testing.T, s *Service, id string) *Task {
	t.Helper()
	for i := 0; i < 200; i++ {
		v, e := s.Get("demo", id)
		if e != nil {
			t.Fatal(e)
		}
		if terminal(v.Status) {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("task not completed")
	return nil
}
func TestRuntimeSelectionSnapshotAndIdempotency(t *testing.T) {
	r := runtimeCapture{make(chan Invocation, 10)}
	cfg := Config{Root: t.TempDir(), Concurrency: 1, QueueCapacity: 2, RuntimeProfiles: map[string]RuntimeProfile{
		"pi-main":     {Engine: "pi", Protocol: "chat_completions", Model: "main-model", BaseURL: "https://example.invalid/v1", APIKeyEnv: "TEST_MODEL_KEY"},
		"codex-cheap": {Engine: "codex", Protocol: "responses", Model: "cheap-model", BaseURL: "https://example.invalid/v1", APIKeyEnv: "TEST_MODEL_KEY"},
		"hidden":      {Engine: "claude", Protocol: "anthropic", Model: "private", BaseURL: "https://example.invalid", APIKeyEnv: "TEST_MODEL_KEY"},
	}, Workflows: []Workflow{{Name: "note", Version: "1", Instructions: "write", Runtime: "pi-main", AllowedRuntimes: []string{"codex-cheap"}, Policy: "workspace-write", TimeoutSeconds: 5}}}
	s, e := New(cfg, r)
	if e != nil {
		t.Fatal(e)
	}
	catalog := s.Workflows()
	if len(catalog) != 1 || len(catalog[0].Runtimes) != 2 {
		t.Fatalf("catalog %+v", catalog)
	}
	b, _ := json.Marshal(catalog)
	if strings.Contains(string(b), "TEST_MODEL_KEY") || strings.Contains(string(b), "example.invalid") {
		t.Fatal("catalog leaks provider configuration")
	}
	for _, id := range []string{"unknown", "hidden", " ", strings.Repeat("x", 129)} {
		if _, e := s.Submit(SubmitRequest{Namespace: "demo", Workflow: "note", Input: "x", Runtime: id, IdempotencyKey: id}); !errors.Is(e, ErrInvalid) {
			t.Fatalf("invalid choice %q: %v", id, e)
		}
	}
	req := SubmitRequest{Namespace: "demo", Workflow: "note", Input: "x", Runtime: "codex-cheap", IdempotencyKey: "once"}
	task, e := s.Submit(req)
	if e != nil {
		t.Fatal(e)
	}
	runtimeDone(t, s, task.ID)
	in := <-r.calls
	if in.Workflow.Engine != "codex" || in.Workflow.Model != "cheap-model" || in.Namespace != "demo" {
		t.Fatalf("wrong invocation %+v", in)
	}
	again, e := s.Submit(req)
	if e != nil || again.ID != task.ID {
		t.Fatal("not idempotent", e)
	}
	changed := req
	changed.Runtime = "pi-main"
	if _, e = s.Submit(changed); !errors.Is(e, ErrConflict) {
		t.Fatal("changed choice not conflict", e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	p := cfg.RuntimeProfiles["codex-cheap"]
	p.BaseURL = "https://changed.invalid/v1"
	p.Model = "changed"
	cfg.RuntimeProfiles["codex-cheap"] = p
	s, e = New(cfg, r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	again, e = s.Submit(req)
	if e != nil || again.ID != task.ID {
		t.Fatal("restart retry drift", e)
	}
	if _, e = s.Resume("demo", task.ID, "continue"); e != nil {
		t.Fatal(e)
	}
	runtimeDone(t, s, task.ID)
	in = <-r.calls
	if in.Workflow.Model != "cheap-model" || in.Workflow.RuntimeSpec.BaseURL != "https://example.invalid/v1" || in.SessionID == "" {
		t.Fatalf("resume drift %+v", in)
	}
	summary, e := s.Summary("demo", task.ID)
	if e != nil || summary.Runtime != "codex-cheap" || summary.Engine != "codex" {
		t.Fatal("summary selection missing", e)
	}
	select {
	case extra := <-r.calls:
		t.Fatalf("duplicate side effect %+v", extra)
	default:
	}
}
func TestRuntimeProfilesRejectProtocolAndCredentialAmbiguity(t *testing.T) {
	for _, p := range []RuntimeProfile{
		{Engine: "codex", Protocol: "chat_completions", GatewayModel: "chat"},
		{Engine: "claude", Protocol: "responses", GatewayModel: "responses"},
		{Engine: "pi", Protocol: "custom", GatewayModel: "chat"},
		{Engine: "pi", Protocol: "chat_completions", GatewayModel: "chat", Model: "other"},
		{Engine: "pi", Protocol: "chat_completions", Model: "x", BaseURL: "https://key@example.invalid", APIKeyEnv: "KEY"},
	} {
		if p.validate() == nil {
			t.Fatalf("accepted invalid profile %+v", p)
		}
	}
	for _, name := range []string{"HOME", "PATH", "CODEX_HOME", "NODE_OPTIONS", "PI_CODING_AGENT_DIR", "OPENCLAW_CONFIG_PATH"} {
		if _, err := NewCommandRunner(map[string]EngineConfig{"codex": {Binary: "/bin/true", EnvAllowlist: []string{name}}}, 0); err == nil {
			t.Fatalf("allowed reserved environment %s", name)
		}
	}
}
