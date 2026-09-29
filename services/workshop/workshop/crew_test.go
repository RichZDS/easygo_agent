//go:build linux

package workshop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"easygo-agent/rpc"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func crewFixture(t *testing.T) (*Service, *Task, Invocation, chan struct{}, chan Invocation) {
	t.Helper()
	started := make(chan Invocation, 2)
	release := make(chan struct{})
	runner := artifactRunnerFunc(func(ctx context.Context, in Invocation, _ func(Event) error) (Result, error) {
		started <- in
		select {
		case <-release:
			return Result{SessionID: fixtureSession}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	})
	s, err := New(Config{Root: t.TempDir(), Concurrency: 1, QueueCapacity: 1, Workflows: []Workflow{{Name: "crew", Version: "1", Engine: "codex", Model: "fixture", Policy: "workspace-write", TimeoutSeconds: 30, Instructions: "build"}}}, runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	task, err := s.Submit(SubmitRequest{Namespace: "owner", Workflow: "crew", Input: "work"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-started:
		return s, task, in, release, started
	case <-time.After(3 * time.Second):
		t.Fatal("runner not started")
	}
	return nil, nil, Invocation{}, nil, nil
}
func TestCrewPostIdempotencyCapacityAndScope(t *testing.T) {
	s, task, in, _, _ := crewFixture(t)
	ctx := context.Background()
	p := CrewPost{ClientID: "same", Kind: "report", Text: "progress"}
	receipts := make(chan CrewReceipt, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := in.Crew.Post(ctx, p)
			if err != nil {
				t.Error(err)
			}
			receipts <- receipt
		}()
	}
	wg.Wait()
	close(receipts)
	var first CrewReceipt
	for receipt := range receipts {
		if first.ID == "" {
			first = receipt
		}
		if receipt != first {
			t.Fatal("duplicate receipt changed")
		}
	}
	p.Text = "different"
	if _, err := in.Crew.Post(ctx, p); !errors.Is(err, ErrConflict) {
		t.Fatal("conflict accepted", err)
	}
	for i := 1; i < 200; i++ {
		if _, err := in.Crew.Post(ctx, CrewPost{ClientID: fmt.Sprint(i), Kind: "report", Text: "progress"}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := in.Crew.Post(ctx, CrewPost{ClientID: "201", Kind: "report", Text: "progress"}); !errors.Is(err, ErrFull) {
		t.Fatal("201st message accepted", err)
	}
	p.Text = "progress"
	if receipt, err := in.Crew.Post(ctx, p); err != nil || receipt != first {
		t.Fatal("retry at capacity rejected", err)
	}
	if _, err := s.Message("other", task.ID, "hello", "key"); !errors.Is(err, ErrNotFound) {
		t.Fatal("namespace escaped", err)
	}
	events, err := s.Events("owner", task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Message != nil && event.Message.Direction == "from_worker" {
			count++
		}
	}
	if count != 200 {
		t.Fatal("wrong persisted count", count)
	}
}
func TestCrewInboxResumeAndOutcomes(t *testing.T) {
	s, task, in, release, started := crewFixture(t)
	first, err := s.Message("owner", task.ID, "already seen", "first")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.Message("owner", task.ID, "already seen", "first")
	if err != nil || duplicate != first {
		t.Fatal("note not idempotent", err)
	}
	if _, err := s.Message("owner", task.ID, "changed", "first"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	inbox, err := in.Crew.Inbox(context.Background(), 0)
	if err != nil || len(inbox.Messages) != 1 || inbox.Messages[0].ID != first.ID || inbox.Next != first.Sequence {
		t.Fatal(inbox, err)
	}
	empty, err := in.Crew.Inbox(context.Background(), inbox.Next)
	if err != nil || len(empty.Messages) != 0 || empty.Next != inbox.Next {
		t.Fatal(empty, err)
	}
	release <- struct{}{}
	task = waitTask(t, s, "owner", task.ID, func(task *Task) bool { return terminal(task.Status) })
	if task.Runs[0].Outcome != "none" {
		t.Fatal(task.Runs[0].Outcome)
	}
	note, err := s.Message("owner", task.ID, "resume instruction", "second")
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.Resume("owner", task.ID, "continue")
	if err != nil {
		t.Fatal(err)
	}
	second := <-started
	want := "continue\n\nUnread messages from the foreman:\n- [" + note.ID + "] resume instruction"
	if second.Input != want || task.Runs[1].Input != want {
		t.Fatal("resume did not snapshot unread notes", second.Input)
	}
	if _, err := in.Crew.Post(context.Background(), CrewPost{ClientID: "old", Kind: "ask", Text: "stale"}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale run capability accepted", err)
	}
	inbox, err = second.Crew.Inbox(context.Background(), first.Sequence)
	if err != nil || len(inbox.Messages) != 1 || inbox.Messages[0].ID != note.ID {
		t.Fatal("cursor inbox must retain already read notes", err)
	}
	for i, kind := range []string{"submit", "ask", "report"} {
		p := CrewPost{ClientID: fmt.Sprint(i), Kind: kind, Text: kind}
		if kind == "submit" {
			p.Claims = &CrewClaims{Tests: "pass"}
		}
		if _, err := second.Crew.Post(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	view, err := s.Summary("owner", task.ID)
	if err != nil || view.Runs[0].Outcome != "" || !reflect.DeepEqual(view.RunIDs, []string{task.Runs[0].ID, task.Runs[1].ID}) {
		t.Fatal("running outcome/run_ids invalid", view, err)
	}
	release <- struct{}{}
	task = waitTask(t, s, "owner", task.ID, func(task *Task) bool { return terminal(task.Status) })
	if task.Runs[1].Outcome != "asked" {
		t.Fatal("last significant message ignored", task.Runs[1].Outcome)
	}
	events, err := s.Events("owner", task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	readCount := 0
	for _, event := range events {
		if event.Read != nil {
			readCount++
		}
	}
	if readCount != 3 {
		t.Fatal("expected inbox read, resume read, cursor reread", readCount)
	}
}
func TestCrewOutcomeVariants(t *testing.T) {
	for kind, want := range map[string]string{"report": "none", "ask": "asked", "blocked": "blocked", "submit": "submitted"} {
		t.Run(kind, func(t *testing.T) {
			s, task, in, release, _ := crewFixture(t)
			p := CrewPost{ClientID: "outcome", Kind: kind, Text: "result"}
			if kind == "submit" {
				p.Claims = &CrewClaims{Tests: "not_run"}
			}
			if _, err := in.Crew.Post(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			release <- struct{}{}
			task = waitTask(t, s, "owner", task.ID, func(task *Task) bool { return terminal(task.Status) })
			if task.Runs[0].Outcome != want {
				t.Fatal(task.Runs[0].Outcome)
			}
		})
	}
}
func TestCrewInboxPageLimit(t *testing.T) {
	s, task, in, _, _ := crewFixture(t)
	for i := 0; i < 51; i++ {
		if _, err := s.Message("owner", task.ID, fmt.Sprint(i), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := in.Crew.Inbox(context.Background(), 0)
	if err != nil || len(page.Messages) != 50 {
		t.Fatal(len(page.Messages), err)
	}
	next, err := in.Crew.Inbox(context.Background(), page.Next)
	if err != nil || len(next.Messages) != 1 || next.Messages[0].Text != "50" {
		t.Fatal(next, err)
	}
}
func TestCrewRelayBoundaries(t *testing.T) {
	_, _, in, _, _ := crewFixture(t)
	gateway := nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
	base, token, stop, err := startModelRelay(context.Background(), gateway, "owner", RuntimeProfile{Protocol: "responses", GatewayModel: "fixture"}, in.Crew)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	base = strings.TrimSuffix(base, "/v1")
	send := func(path, method, key, body string) int {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if key != "" {
			req.Header.Set("X-Api-Key", key)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return response.StatusCode
	}
	valid := `{"client_id":"x","kind":"report","text":"ok"}`
	for _, key := range []string{"", uuid.NewString()} {
		if code := send("/crew/messages", "POST", key, valid); code != 401 {
			t.Fatal("auth", code)
		}
	}
	for _, path := range []string{"/crew/missing", "/crew/messages?x=1", "/crew/messages?"} {
		if code := send(path, "POST", token, valid); code != 404 {
			t.Fatal(path, code)
		}
	}
	if code := send("/crew/messages", "GET", token, valid); code != 404 {
		t.Fatal(code)
	}
	for _, raw := range []string{
		`{}`, `null`, `[]`, valid + ` {}`, `{"client_id":"x","kind":"report","text":"ok","unknown":1}`,
		`{"client_id":"x","kind":"report","text":"ok","claims":null}`, `{"client_id":"x","kind":"report","text":"ok","claims":{"tests":"pass"}}`,
		`{"client_id":"x","kind":"submit","text":"ok"}`, `{"client_id":"x","kind":"submit","text":"ok","claims":{"tests":"maybe"}}`,
		`{"client_id":"x","kind":"note","text":"ok"}`, `{"client_id":"bad.id","kind":"report","text":"ok"}`, `{"client_id":"","kind":"report","text":"ok"}`,
		`{"client_id":"` + strings.Repeat("x", 65) + `","kind":"report","text":"ok"}`, `{"client_id":"x","kind":"report","text":""}`,
		`{"client_id":"x","kind":"report","text":"` + strings.Repeat("x", 8193) + `"}`, `{"client_id":"x","kind":"report","text":"` + string([]byte{255}) + `"}`,
		valid + strings.Repeat(" ", 16384),
	} {
		if code := send("/crew/messages", "POST", token, raw); code != 400 {
			t.Fatalf("invalid request accepted: HTTP %d", code)
		}
	}
	if code := send("/crew/messages", "POST", token, valid); code != 200 {
		t.Fatal(code)
	}
	if code := send("/crew/messages", "POST", token, strings.Replace(valid, "ok", "changed", 1)); code != 409 {
		t.Fatal(code)
	}
	for _, raw := range []string{`{"after":-1}`, `{"after":1.5}`, `{"after":null}`, `{"after":18446744073709551616}`, `{"wrong":0}`} {
		if code := send("/crew/inbox", "POST", token, raw); code != 400 {
			t.Fatal(code)
		}
	}
	for _, raw := range []string{`{}`, `{"after":18446744073709551615}`} {
		if code := send("/crew/inbox", "POST", token, raw); code != 200 {
			t.Fatal(code)
		}
	}
	boundary, _ := json.Marshal(CrewPost{ClientID: strings.Repeat("x", 64), Kind: "submit", Text: strings.Repeat("界", 2730) + "ab", Claims: &CrewClaims{Tests: "pass"}})
	if code := send("/crew/messages", "POST", token, string(boundary)); code != 200 {
		t.Fatal("valid byte boundary rejected", code)
	}
	for i := 2; i < 200; i++ {
		raw, _ := json.Marshal(CrewPost{ClientID: fmt.Sprint(i), Kind: "report", Text: "ok"})
		if code := send("/crew/messages", "POST", token, string(raw)); code != 200 {
			t.Fatal(i, code)
		}
	}
	if code := send("/crew/messages", "POST", token, strings.Replace(valid, `"x"`, `"extra"`, 1)); code != 429 {
		t.Fatal(code)
	}
}
func TestCrewRunLimitAndSummaryIDs(t *testing.T) {
	s, task, _, release, _ := crewFixture(t)
	release <- struct{}{}
	task = waitTask(t, s, "owner", task.ID, func(task *Task) bool { return terminal(task.Status) })
	s.mu.Lock()
	err := s.db.Update(func(tx *bolt.Tx) error {
		for len(task.Runs) < 256 {
			task.Runs = append(task.Runs, Run{ID: uuid.NewString(), Status: Succeeded})
		}
		return putTask(tx, task)
	})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resume("owner", task.ID, "again"); !errors.Is(err, ErrRunLimit) {
		t.Fatal("run limit not enforced", err)
	}
	view, err := s.Summary("owner", task.ID)
	if err != nil || len(view.RunIDs) != 256 {
		t.Fatal(err)
	}
	for i, run := range task.Runs {
		if view.RunIDs[i] != run.ID {
			t.Fatal("run ordering changed")
		}
	}
	after, err := s.Get("owner", task.ID)
	if err != nil || len(after.Runs) != 256 || after.Status != Succeeded {
		t.Fatal("rejected resume mutated task", err)
	}
}
func TestWorkerPackPrompt(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "roles"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "roles", "worker.md")
	if value, err := readWorkerInstructions(dir); err != nil || value != "" {
		t.Fatal(value, err)
	}
	for _, size := range []int{16384, 16385} {
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0600); err != nil {
			t.Fatal(err)
		}
		text, err := readWorkerInstructions(dir)
		if size == 16384 && (err != nil || len(text) != size) {
			t.Fatal(err)
		}
		if size == 16385 && !errors.Is(err, ErrInvalid) {
			t.Fatal("oversize accepted", err)
		}
	}
	in := Invocation{Workflow: Workflow{Instructions: "workflow"}, Input: "input", WorkerInstructions: "worker"}
	if invocationPrompt(in) != "worker\n\nworkflow\n\nUser input:\ninput" {
		t.Fatal(invocationPrompt(in))
	}
	in.WorkerInstructions = ""
	if invocationPrompt(in) != "workflow\n\nUser input:\ninput" {
		t.Fatal("legacy prompt changed")
	}
}

func TestCrewHostEnvironmentAndPackInvocation(t *testing.T) {
	gateway := nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
	runner := &CommandRunner{gateway: gateway}
	in := Invocation{Namespace: "task-namespace", Workflow: Workflow{RuntimeSpec: &RuntimeProfile{Engine: "codex", Protocol: "responses", GatewayModel: "fixture"}}}
	env := map[string]string{"HOME": t.TempDir()}
	_, secrets, cleanup, err := runner.configureRuntime(context.Background(), in, []string{"exec", "-"}, env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(secrets) != 1 || env["EASYGO_CREW_TOKEN"] != secrets[0] || !strings.HasSuffix(env["EASYGO_CREW_URL"], "/crew") {
		t.Fatal("host crew capability not injected")
	}
	in.Workflow.RuntimeSpec = nil
	env = map[string]string{}
	if _, _, _, err := runner.configureRuntime(context.Background(), in, []string{"exec"}, env); err != nil {
		t.Fatal(err)
	}
	if env["EASYGO_CREW_URL"] != "" || env["EASYGO_CREW_TOKEN"] != "" {
		t.Fatal("crew injected without relay")
	}
	for _, name := range []string{"EASYGO_CREW_TOKEN", "EASYGO_CREW_URL"} {
		if !reservedEnvironment(name) {
			t.Fatal("crew environment can be overridden", name)
		}
	}
	pack := t.TempDir()
	if err := os.Mkdir(filepath.Join(pack, "roles"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pack, "roles", "worker.md"), []byte("worker instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	captured := make(chan Invocation, 1)
	s, err := New(Config{Root: t.TempDir(), PackDir: pack, Concurrency: 1, Workflows: []Workflow{{Name: "pack", Version: "1", Engine: "codex", Model: "fixture", Policy: "workspace-write", Instructions: "workflow", TimeoutSeconds: 5}}}, artifactRunnerFunc(func(_ context.Context, in Invocation, _ func(Event) error) (Result, error) {
		captured <- in
		return Result{SessionID: fixtureSession}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task, err := s.Submit(SubmitRequest{Namespace: "owner", Workflow: "pack", Input: "input"})
	if err != nil {
		t.Fatal(err)
	}
	actual := <-captured
	if invocationPrompt(actual) != "worker instructions\n\nworkflow\n\nUser input:\ninput" {
		t.Fatal("pack not passed to runner")
	}
	waitTask(t, s, "owner", task.ID, func(task *Task) bool { return terminal(task.Status) })
}

// TestFinishCrewOutcomeRecomputesFalseGreen exercises the truth-table
// recompute finishCrewOutcome now does after finish() may have rewritten
// Acceptance.State: a stale false_green from before the state change can
// never survive alongside a state that no longer satisfies the truth table.
// It also checks the ordinary failed and cancelled paths, which already
// computed the right value through acceptanceState, are unaffected.
func TestFinishCrewOutcomeRecomputesFalseGreen(t *testing.T) {
	s, err := New(Config{Root: t.TempDir(), Concurrency: 1, Workflows: []Workflow{{Name: "test", Version: "1", Engine: "codex", Model: "fixture", Instructions: "offline", Policy: "workspace-write", TimeoutSeconds: 5}}}, artifactRunnerFunc(func(context.Context, Invocation, func(Event) error) (Result, error) {
		return Result{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cases := []struct {
		name           string
		seededState    string
		seededGreen    bool
		submitTests    string
		finishStatus   Status
		wantState      string
		wantFalseGreen bool
	}{
		{
			name: "stale-false-green-cleared-when-interrupted", seededState: "failed", seededGreen: true, submitTests: "pass",
			finishStatus: Interrupted, wantState: "interrupted", wantFalseGreen: false,
		},
		{
			name: "normal-failed-path-unaffected", seededState: "failed", seededGreen: false, submitTests: "pass",
			finishStatus: Succeeded, wantState: "failed", wantFalseGreen: true,
		},
		{
			name: "normal-cancelled-path-unaffected", seededState: "running", seededGreen: false, submitTests: "pass",
			finishStatus: Cancelled, wantState: "cancelled", wantFalseGreen: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runID := uuid.NewString()
			task := &Task{ID: uuid.NewString(), Namespace: "owner", Status: Running,
				Workflow: Workflow{Acceptance: &AcceptanceConfig{Checks: []AcceptanceCheck{{Name: "unit", Command: []string{"/unit"}, TimeoutSeconds: 1}}}},
				Runs:     []Run{{ID: runID, Status: Running, Acceptance: &Acceptance{State: c.seededState, FalseGreen: c.seededGreen, Evidence: []Evidence{}}}},
			}
			s.mu.Lock()
			err := s.db.Update(func(tx *bolt.Tx) error {
				if c.submitTests != "" {
					if err := appendEvent(tx, task, Event{Kind: "crew.message", Message: &CrewMessage{ID: uuid.NewString(), Direction: "from_worker", Kind: "submit", Text: "ready", Claims: &CrewClaims{Tests: c.submitTests}}}); err != nil {
						return err
					}
				}
				return putTask(tx, task)
			})
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			err = s.db.Update(func(tx *bolt.Tx) error {
				finish(task, c.finishStatus, "test")
				return finishCrewOutcome(tx, task)
			})
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			got := task.Runs[0].Acceptance
			if got.State != c.wantState || got.FalseGreen != c.wantFalseGreen {
				t.Fatalf("state=%s false_green=%v, want state=%s false_green=%v", got.State, got.FalseGreen, c.wantState, c.wantFalseGreen)
			}
		})
	}
}
