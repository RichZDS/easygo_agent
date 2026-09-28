package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easygo-agent/rpc"
	"easygo-agent/rpc/rpctest"
	"easygo-agent/services/workshop/workshop"
)

func TestAllWorkshopMethodsOverTLS(t *testing.T) {
	p := rpctest.NewPKI(t)
	identity := p.Issue("workshop", false)
	caller := p.Issue("loop", false)
	// A real, local subprocess speaks the native Codex protocol. No live CLI/model.
	script := filepath.Join(t.TempDir(), "fixture")
	body := `#!/bin/sh
prompt=$(cat)
printf '%s\n' '{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}'
case "$prompt" in *hang*) sleep 30;; esac
printf 'artifact' > note.md
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"fixture answer"}}' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`
	if e := os.WriteFile(script, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	s, service, e := New(Config{ServerConfig: rpc.ServerConfig{Listen: "127.0.0.1:0", TLS: identity, Authorization: []rpc.Authorization{{ID: "loop", CertFile: caller.CertFile, Methods: []string{"health", "workshop.workflows", "workshop.submit", "workshop.get", "workshop.list", "workshop.cancel", "workshop.resume", "workshop.result", "workshop.events"}, Namespaces: []string{"tenant-a", "tenant-b"}}}}, Workshop: workshop.Config{Root: t.TempDir(), Concurrency: 1, QueueCapacity: 8, Engines: map[string]workshop.EngineConfig{"codex": {Binary: script}}, Workflows: []workshop.Workflow{{Name: "note", Version: "1", Instructions: "write note", Engine: "codex", Model: "fixture", Policy: "workspace-write", TimeoutSeconds: 10, Artifacts: []string{"note.md"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	ts := rpctest.Start(t, s)
	c := rpctest.Client(t, caller, identity.CertFile)
	call := func(method, params string) any {
		t.Helper()
		status, out := rpctest.Call(t, c, ts.URL, "workshop."+method, params)
		if status != 200 || out.Error != nil {
			t.Fatalf("%s: status %d %+v", method, status, out)
		}
		raw, _ := json.Marshal(out.Result)
		if strings.Contains(string(raw), `"workspace":`) || strings.Contains(string(raw), script) {
			t.Fatalf("private configuration leaked: %s", raw)
		}
		return out.Result
	}
	taskParams := func(id string) string { return fmt.Sprintf(`{"namespace":"tenant-a","task_id":%q}`, id) }
	wait := func(id string, status string) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			out := call("get", taskParams(id)).(map[string]any)
			if out["status"] == status {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("task %s did not reach %s", id, status)
	}
	catalog := call("workflows", `{"namespace":"tenant-a"}`).([]any)
	if len(catalog) != 1 {
		t.Fatal("catalog missing")
	}
	submit := `{"namespace":"tenant-a","workflow":"note","input":"hello","idempotency_key":"key-1"}`
	first := call("submit", submit).(map[string]any)
	id := first["id"].(string)
	again := call("submit", submit).(map[string]any)
	if again["id"] != id {
		t.Fatal("idempotency lost")
	}
	wait(id, "succeeded")
	result := call("result", taskParams(id)).(map[string]any)
	if result["text"] != "fixture answer" || result["eof"] != true {
		t.Fatalf("result %+v", result)
	}
	page := call("list", `{"namespace":"tenant-a","limit":1}`).(map[string]any)
	if len(page["tasks"].([]any)) != 1 {
		t.Fatal("list missing task")
	}
	events := call("events", taskParams(id)).([]any)
	if len(events) == 0 {
		t.Fatal("events empty")
	}
	call("resume", fmt.Sprintf(`{"namespace":"tenant-a","task_id":%q,"input":"continue"}`, id))
	wait(id, "succeeded")
	summary := call("get", taskParams(id)).(map[string]any)
	if summary["run_count"] != float64(2) {
		t.Fatalf("resume failed %+v", summary)
	}
	hanging := call("submit", `{"namespace":"tenant-a","workflow":"note","input":"hang","idempotency_key":"key-2"}`).(map[string]any)
	hangID := hanging["id"].(string)
	wait(hangID, "running")
	call("cancel", taskParams(hangID))
	wait(hangID, "cancelled")
	for _, method := range []string{"get", "cancel", "resume", "result", "events"} {
		params := fmt.Sprintf(`{"namespace":"tenant-b","task_id":%q`, id)
		if method == "resume" {
			params += `,"input":"steal"`
		}
		params += "}"
		_, out := rpctest.Call(t, c, ts.URL, "workshop."+method, params)
		if out.Error == nil || out.Error.Code != -32004 {
			t.Fatalf("cross-namespace %s: %+v", method, out)
		}
		wire, _ := json.Marshal(out)
		if strings.Contains(string(wire), `"result"`) {
			t.Fatal("error contains a result")
		}
	}
	empty := call("list", `{"namespace":"tenant-b"}`).(map[string]any)
	if len(empty["tasks"].([]any)) != 0 {
		t.Fatal("list leaks other namespace")
	}
	for _, tc := range []struct {
		method, params string
		code           int
	}{
		{"submit", `{"namespace":"tenant-a","workflow":"note","input":"hello"}`, -32602},
		{"submit", `{"namespace":"tenant-a","workflow":"note","input":"different","idempotency_key":"key-1"}`, -32009},
		{"list", `{"namespace":"tenant-a","limit":0}`, -32602},
		{"list", `{"namespace":"tenant-a","limit":null}`, -32602},
		{"list", `{"namespace":"tenant-a","offset":-1}`, -32602},
		{"list", `{"namespace":"tenant-a","offset":1.5}`, -32602},
		{"events", fmt.Sprintf(`{"namespace":"tenant-a","task_id":%q,"after":-1}`, id), -32602},
		{"result", fmt.Sprintf(`{"namespace":"tenant-a","task_id":%q,"limit":32769}`, id), -32602},
		{"get", fmt.Sprintf(`{"namespace":"tenant-a","task_id":%q,"unknown":1}`, id), -32602},
	} {
		_, out := rpctest.Call(t, c, ts.URL, "workshop."+tc.method, tc.params)
		if out.Error == nil || out.Error.Code != tc.code {
			t.Fatalf("%s %s %+v", tc.method, tc.params, out)
		}
	}
	for _, path := range []string{"/v1/tasks", "/v1/workflows"} {
		resp, e := c.Get(ts.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatal("legacy route mounted")
		}
	}
}
func TestConfigRejectsBearerAndUnknown(t *testing.T) {
	for _, raw := range []string{`{"workshop":{"bearer_token_env":"TOKEN"}}`, `{"extra":1}`, `{"listen":":1","listen":":2"}`} {
		if _, e := LoadConfig(strings.NewReader(raw)); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
