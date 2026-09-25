package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	agentruntime "easygo-agent/internal/agent/runtime"
	"github.com/cloudwego/eino/components/tool"
)

func workshopContext(user, session string, internal bool) context.Context {
	return agentruntime.WithInvocationIdentity(context.Background(), agentruntime.InvocationIdentity{Username: user, SessionID: session, RunID: "run-test", Internal: internal})
}

func workshopToolsFor(t *testing.T, endpoint string) map[string]tool.InvokableTool {
	t.Helper()
	items, err := NewWorkshopTools(WorkshopConfig{BaseURL: endpoint, AuthToken: "operator-test-token"})
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]tool.InvokableTool)
	for _, item := range items {
		info, err := item.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out[info.Name] = item.(tool.InvokableTool)
	}
	return out
}

func TestWorkshopToolsDeriveOwnerAndHideHostDetails(t *testing.T) {
	ctx := workshopContext("alice", "s1", false)
	wantNS, err := workshopNamespace(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer operator-test-token" || r.URL.Query().Get("namespace") != wantNS {
			t.Error("trusted credentials or owner missing")
		}
		if r.URL.Query().Get("view") != "summary" {
			t.Error("tool did not request bounded summary")
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tasks" {
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload["namespace"] != "" || payload["idempotency_key"] != "once" {
				t.Error("untrusted submit owner or missing dedup key")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "task1", "namespace": wantNS, "status": "succeeded", "workspace": "/private/host/path", "session_id": "native-secret-id", "workflow": map[string]string{"instructions": "private workflow prompt"}, "runs": []map[string]any{{"id": "attempt1", "status": "succeeded", "text": "proof", "artifacts": []map[string]any{{"path": "report.txt", "size": 5, "sha256": "abc"}}}}})
	}))
	defer server.Close()
	items := workshopToolsFor(t, server.URL)
	out, err := items["workshop_submit"].InvokableRun(ctx, `{"workflow":"report","input":"hello","idempotency_key":"once","namespace":"attacker","base_url":"http://attacker"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{wantNS, "private/host", "native-secret-id", "private workflow"} {
		if strings.Contains(out, hidden) {
			t.Fatalf("response leaks host detail %q", hidden)
		}
	}
	if !strings.Contains(out, "report.txt") || !strings.Contains(out, "proof") {
		t.Fatalf("result or artifact missing: %s", out)
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected request count")
	}
	for _, c := range []context.Context{context.Background(), workshopContext("alice", "s1", true)} {
		if _, err := items["workshop_submit"].InvokableRun(c, `{"workflow":"report","input":"hello","idempotency_key":"once"}`); err == nil {
			t.Fatal("untrusted mutation allowed")
		}
	}
	if _, err := items["workshop_get"].InvokableRun(ctx, `{"task_id":"../escape"}`); err == nil {
		t.Fatal("path traversal allowed")
	}
	if calls.Load() != 1 {
		t.Fatal("invalid calls reached workshop")
	}
	otherNS, _ := workshopNamespace(workshopContext("bob", "s1", false), false)
	if otherNS == wantNS {
		t.Fatal("users shared namespace")
	}
	otherNS, _ = workshopNamespace(workshopContext("alice", "s2", false), false)
	if otherNS == wantNS {
		t.Fatal("sessions shared namespace")
	}
}

func TestWorkshopPagedToolsPreserveQueryAndCheckEveryOwner(t *testing.T) {
	ctx := workshopContext("alice", "paged-session", false)
	namespace, err := workshopNamespace(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var wrongOwner atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("namespace") != namespace || len(query["namespace"]) != 1 || query.Get("view") != "summary" {
			t.Error("scope/view query lost")
		}
		if r.URL.Path == "/v1/tasks/task/result" {
			if query.Get("run_id") != "attempt+one" || query.Get("offset") != "3" || query.Get("limit") != "4" {
				t.Errorf("result query changed: %v", query)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "task", "run_id": "attempt+one", "text": "界", "offset": 3, "next_offset": 6, "total_bytes": 6, "eof": true})
			return
		}
		if query.Get("offset") != "1" || query.Get("limit") != "2" {
			t.Errorf("list query changed: %v", query)
		}
		second := namespace
		if wrongOwner.Load() {
			second = "someone-else"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{{"id": "first", "namespace": namespace, "status": "succeeded", "run_count": 5}, {"id": "second", "namespace": second, "status": "running", "run_count": 2}}, "offset": 1, "next_offset": 3})
	}))
	defer server.Close()
	items := workshopToolsFor(t, server.URL)
	value, err := items["workshop_list"].InvokableRun(ctx, `{"offset":1,"limit":2,"namespace":"attacker"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(value, "namespace") || !strings.Contains(value, `"run_count":5`) || !strings.Contains(value, `"next_offset":3`) {
		t.Fatal("list metadata or scope projection incorrect")
	}
	value, err = items["workshop_result"].InvokableRun(ctx, `{"task_id":"task","run_id":"attempt+one","offset":3,"limit":4,"namespace":"attacker"}`)
	if err != nil || !strings.Contains(value, "界") {
		t.Fatalf("result page: %s %v", value, err)
	}
	wrongOwner.Store(true)
	if _, err = items["workshop_list"].InvokableRun(ctx, `{"offset":1,"limit":2}`); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("later list item owner not checked: %v", err)
	}
}

func TestWorkshopTransportRefusesRedirectAndWrongOwner(t *testing.T) {
	var leaked atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	ctx := workshopContext("alice", "s1", false)
	_, err := workshopToolsFor(t, redirect.URL)["workshop_get"].InvokableRun(ctx, `{"task_id":"task1"}`)
	if err == nil || leaked.Load() != 0 {
		t.Fatal("workshop redirect followed")
	}
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"task1","namespace":"other-session","status":"succeeded"}`))
	}))
	defer wrong.Close()
	_, err = workshopToolsFor(t, wrong.URL)["workshop_get"].InvokableRun(ctx, `{"task_id":"task1"}`)
	if err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("wrong owner was accepted: %v", err)
	}
}
