package remotetui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"easygo-agent/internal/clientapi"
)

type fixture struct {
	t           *testing.T
	mu          sync.Mutex
	status      string
	runtime     string
	key         string
	cancelCalls int
	polls       int
	after       []float64
	retry       bool
	envelope    bool
	methods     []string
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Origin") != "http://"+r.Host {
		f.t.Error("missing Origin")
		w.WriteHeader(403)
		return
	}
	if r.URL.Path == "/api/login" {
		var p map[string]string
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p["email"] != "alice@example.test" || p["password"] != "dummy-test-password" {
			w.WriteHeader(401)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "dummy-session", Path: "/", HttpOnly: true})
		fmt.Fprint(w, `{"user":{"id":"alice"}}`)
		return
	}
	c, err := r.Cookie("session")
	if err != nil || c.Value != "dummy-session" {
		w.WriteHeader(401)
		return
	}
	if r.URL.Path == "/api/logout" {
		w.WriteHeader(204)
		return
	}
	var req struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		w.WriteHeader(400)
		return
	}
	if _, ok := req.Params["namespace"]; ok {
		f.t.Error("client must not send namespace")
	}
	f.methods = append(f.methods, req.Method)
	run := func() any {
		return map[string]any{"id": "run-1", "session_id": "session-1", "status": f.status, "created_at": "2026-09-28T00:00:00Z", "result": map[string]any{"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "finished remotely"}}}}}
	}
	var out any
	switch req.Method {
	case "agent.session.create":
		out = map[string]any{"id": "session-1"}
	case "agent.session.list":
		out = map[string]any{"sessions": []any{map[string]any{"id": "session-1"}}}
	case "agent.session.history":
		out = map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "persisted question"}}}}, "runs": []any{run()}, "next_after": nil}
	case "agent.run.start":
		f.runtime, _ = req.Params["workshop_runtime"].(string)
		f.key, _ = req.Params["idempotency_key"].(string)
		out = run()
	case "agent.run.get":
		out = run()
	case "agent.run.cancel":
		f.cancelCalls++
		f.status = "canceled"
		out = run()
	case "agent.run.events":
		f.polls++
		after, _ := req.Params["after"].(float64)
		f.after = append(f.after, after)
		if f.retry && f.polls == 1 {
			w.WriteHeader(503)
			return
		}
		events := []any{}
		if after == 0 {
			events = append(events, map[string]any{"seq": 1, "kind": "delta", "data": map[string]any{"event": map[string]any{"type": "text_delta", "delta": "finished "}}})
		} else {
			f.status = "completed"
		}
		out = map[string]any{"events": events, "next_after": nil}
	case "agent.skills.list":
		out = map[string]any{"skills": []any{map[string]any{"name": "review", "description": "fixture"}}}
	default:
		w.WriteHeader(400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if f.envelope {
		out = map[string]any{"result": out}
	}
	_ = json.NewEncoder(w).Encode(out)
}
func setup(t *testing.T, envelope bool) (*Client, *fixture) {
	t.Helper()
	f := &fixture{t: t, status: "running", envelope: envelope}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	c, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Login(context.Background(), "alice@example.test", "dummy-test-password"); err != nil {
		t.Fatal(err)
	}
	return c, f
}
func TestPublicAuthOriginCookieAndNamespaces(t *testing.T) {
	for _, envelope := range []bool{false, true} {
		t.Run(fmt.Sprint(envelope), func(t *testing.T) {
			c, _ := setup(t, envelope)
			ctx := context.Background()
			s, err := c.CreateSession(ctx)
			if err != nil || s.ID != "session-1" {
				t.Fatalf("create: %v %v", s, err)
			}
			sessions, err := c.Sessions(ctx)
			if err != nil || len(sessions) != 1 {
				t.Fatal(sessions, err)
			}
			h, err := c.History(ctx, s.ID, 0)
			if err != nil || h.Messages[0].Content[0].Text != "persisted question" {
				t.Fatal(h, err)
			}
			var skills any
			if err = c.RPC(ctx, "agent.skills.list", nil, &skills); err != nil {
				t.Fatal(err)
			}
			if err = c.RPC(ctx, "agent.session.list", map[string]any{"namespace": "bob"}, &skills); err == nil {
				t.Fatal("accepted client namespace")
			}
			if err = c.Logout(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestQueueReconnectRuntimeCancelAndClose(t *testing.T) {
	c, f := setup(t, false)
	q := NewQueue(c, "codex-fixture")
	defer q.Close()
	ctx := context.Background()
	runs, err := q.List(ctx, "ignored-identity", "session-1", 100)
	if err != nil || len(runs) != 1 {
		t.Fatalf("reconnect: %v %v", runs, err)
	}
	r, h, err := q.Submit(ctx, "ignored-identity", "session-1", "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "run-1" || h.Run().ID != r.ID {
		t.Fatal("run mismatch")
	}
	f.mu.Lock()
	if f.runtime != "codex-fixture" || f.key == "" {
		t.Error("runtime/idempotency missing")
	}
	f.mu.Unlock()
	if _, err = q.Cancel(ctx, "", "wrong-session", r.ID); err == nil {
		t.Fatal("wrong session cancel allowed")
	}
	canceled, err := q.Cancel(ctx, "", "session-1", r.ID)
	if err != nil || canceled.Status != clientapi.RunCanceled {
		t.Fatal(canceled, err)
	}
	h.Close()
	q.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelCalls != 1 {
		t.Fatalf("close canceled remote work: %d", f.cancelCalls)
	}
}
func TestPollReconnectCursorAndTerminalText(t *testing.T) {
	c, f := setup(t, true)
	f.retry = true
	q := NewQueue(c, "")
	q.poll = time.Millisecond
	defer q.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := q.Subscribe(ctx, "", "session-1", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var delta, final string
	for e := range s.Events() {
		if e.Kind == clientapi.EventTextDelta {
			delta += e.Text
		}
		if e.Kind == clientapi.EventCompleted {
			final = e.Text
		}
	}
	if delta != "finished " || final != "finished remotely" {
		t.Fatalf("transcript %q / %q", delta, final)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.polls < 3 || f.after[0] != 0 || f.after[1] != 0 || f.after[2] != 1 {
		t.Fatal(f.after)
	}
	if f.cancelCalls != 0 {
		t.Fatal("subscription canceled remote run")
	}
}
func TestCloseStopsSubscriptionsWithoutCancelingServer(t *testing.T) {
	c, f := setup(t, false)
	q := NewQueue(c, "")
	q.poll = time.Hour
	s, err := q.Subscribe(context.Background(), "", "session-1", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	q.Close()
	for range s.Events() {
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelCalls != 0 {
		t.Fatal("Close canceled server task")
	}
}
func TestURLAndRedirectSafety(t *testing.T) {
	for _, u := range []string{"http://example.com", "https://user:password@example.com", "https://example.com/path", "https://example.com?token=secret"} {
		if _, err := NewClient(u); err == nil {
			t.Fatalf("accepted %s", u)
		}
	}
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed login redirect") }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer server.Close()
	c, _ := NewClient(server.URL)
	if err := c.Login(context.Background(), "dummy", "dummy"); err == nil {
		t.Fatal("redirect accepted")
	}
}
