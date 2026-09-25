//go:build linux

package tools

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"easygo-agent/pkg/workshop"
)

// Extends the original P2 reproduction without changing its saved source/logs:
// a real child returns 1.1 MiB, accepted by workshop's default 4 MiB allowance.
func TestWorkshopLargeResultThroughHTTPTools(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fixture")
	script := `#!/usr/bin/python3
import json, sys
prompt = sys.stdin.read()
text = ('a界🙂' * 3000) if 'followup' in prompt else ('x' * (1100 * 1024))
print(json.dumps({'type':'thread.started','thread_id':'11111111-2222-4333-8444-555555555555'}))
print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':text}}))
print(json.dumps({'type':'turn.completed','usage':{'input_tokens':1,'output_tokens':1}}))
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	service, err := workshop.New(workshop.Config{Root: t.TempDir(), Concurrency: 1, QueueCapacity: 1, Engines: map[string]workshop.EngineConfig{"codex": {Binary: binary}}, Workflows: []workshop.Workflow{{Name: "large", Version: "v1", Engine: "codex", Model: "fixture", Policy: "read-only", TimeoutSeconds: 5, Instructions: "fixture"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := httptest.NewServer(workshop.Handler(service, "operator-test-token"))
	defer server.Close()
	ctx := workshopContext("alice", "large-result", false)
	ns, err := workshopNamespace(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	items := workshopToolsFor(t, server.URL)
	call := func(name string, args any, out any) string {
		t.Helper()
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		value, err := items[name].InvokableRun(ctx, string(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(value) >= 512*1024 {
			t.Fatalf("%s returned unbounded data: %d", name, len(value))
		}
		if strings.Contains(value, ns) {
			t.Fatal("tool exposed namespace")
		}
		if err = json.Unmarshal([]byte(value), out); err != nil {
			t.Fatal(err)
		}
		return value
	}
	var task workshopTaskView
	call("workshop_submit", map[string]string{"workflow": "large", "input": "fixture", "idempotency_key": "once"}, &task)
	id := task.ID
	wait := func() {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			state, err := service.Get(ns, id)
			if err != nil {
				t.Fatal(err)
			}
			if state.Status == workshop.Succeeded {
				return
			}
			if state.Status == workshop.Failed {
				t.Fatalf("fixture failed: %+v", state.Runs)
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("fixture did not finish")
	}
	wait()
	call("workshop_get", map[string]string{"task_id": id}, &task)
	if task.Status != "succeeded" || task.RunCount != 1 || len(task.Runs) != 1 || !task.Runs[0].TextTruncated || task.Runs[0].TextBytes != 1100*1024 || len(task.Runs[0].Text) != 16*1024 {
		t.Fatal("large task summary incomplete or wrong truncation metadata")
	}
	originalRun := task.Runs[0].ID
	// Historical attempts remain addressable after another attempt finishes.
	call("workshop_resume", map[string]string{"task_id": id, "input": "followup"}, &task)
	wait()
	call("workshop_get", map[string]string{"task_id": id}, &task)
	if task.RunCount != 2 || len(task.Runs) != 1 || task.Runs[0].ID == originalRun || !task.Runs[0].TextTruncated || !utf8.ValidString(task.Runs[0].Text) {
		t.Fatal("latest attempt summary or UTF8 preview incorrect")
	}
	latestRun := task.Runs[0].ID
	for _, tc := range []struct {
		run, want string
		limit     int
	}{{originalRun, strings.Repeat("x", 1100*1024), 32768}, {latestRun, strings.Repeat("a界🙂", 3000), 8191}} {
		var joined strings.Builder
		offset := 0
		for {
			var page workshopResultPage
			call("workshop_result", map[string]any{"task_id": id, "run_id": tc.run, "offset": offset, "limit": tc.limit}, &page)
			if page.TaskID != id || page.RunID != tc.run || page.Offset != offset || page.TotalBytes != len(tc.want) || !utf8.ValidString(page.Text) || page.NextOffset != offset+len(page.Text) {
				t.Fatal("invalid result cursor or UTF8")
			}
			joined.WriteString(page.Text)
			if page.EOF {
				break
			}
			if page.NextOffset <= offset {
				t.Fatal("result stalled")
			}
			offset = page.NextOffset
		}
		if joined.String() != tc.want {
			t.Fatal("result reconstruction lost or duplicated bytes")
		}
	}
	var page workshopListPage
	raw := call("workshop_list", map[string]int{"offset": 0, "limit": 1}, &page)
	if len(page.Tasks) != 1 || page.Tasks[0].RunCount != 2 || page.NextOffset != nil || strings.Contains(raw, `"text":`) || strings.Contains(raw, `"artifacts":`) {
		t.Fatal("list includes payload or loses history count")
	}
	// The full result is read-only and allowed on internal notification contexts.
	internal := workshopContext("alice", "large-result", true)
	args, _ := json.Marshal(map[string]any{"task_id": id, "run_id": latestRun, "limit": 4})
	if _, err = items["workshop_result"].InvokableRun(internal, string(args)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workshop_get", "workshop_result"} {
		args, _ := json.Marshal(map[string]string{"task_id": id})
		if _, err = items[name].InvokableRun(workshopContext("bob", "large-result", false), string(args)); err == nil {
			t.Fatalf("%s crossed owner scope", name)
		}
	}
	for _, args := range []string{`{"task_id":"` + id + `","run_id":"missing"}`, `{"task_id":"` + id + `","offset":2}`, `{"task_id":"` + id + `","limit":3}`, `{"task_id":"` + id + `","limit":32769}`, `{"task_id":"` + id + `","limit":"bad"}`} {
		if _, err = items["workshop_result"].InvokableRun(ctx, args); err == nil {
			t.Fatalf("invalid result request accepted: %s", args)
		}
	}
}
