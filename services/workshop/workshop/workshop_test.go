//go:build linux

package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

const fixtureSession = "11111111-2222-4333-8444-555555555555"

// The test executable is also a real child CLI fixture. It receives exactly the
// production argv/stdin/cwd/env and emits representative native JSONL. No model
// or provider network connection is made by any workshop test.
func init() {
	if os.Getenv("WORKSHOP_FIXTURE_CHILD") == "1" {
		for {
			time.Sleep(time.Second)
		}
	}
	if os.Getenv("WORKSHOP_FIXTURE") != "1" {
		return
	}
	fixtureMain()
	os.Exit(0)
}

func fixtureMain() {
	prompt, _ := io.ReadAll(os.Stdin)
	cwd, _ := os.Getwd()
	capture, _ := json.Marshal(map[string]any{"args": os.Args[1:], "stdin": string(prompt), "cwd": cwd, "unlisted_secret": os.Getenv("UNLISTED_SECRET")})
	_ = os.WriteFile("capture.json", capture, 0600)
	parts := strings.Split(string(prompt), "\nUser input:\n")
	mode := parts[len(parts)-1]
	codex := len(os.Args) > 1 && os.Args[1] == "exec"
	encoder := json.NewEncoder(os.Stdout)
	send := func(v any) { _ = encoder.Encode(v) }
	if mode == "no-session" {
		// Deliberately omit the native session identity.
	} else if codex {
		send(map[string]any{"type": "thread.started", "thread_id": fixtureSession})
	} else {
		send(map[string]any{"type": "system", "subtype": "init", "session_id": fixtureSession})
	}
	switch mode {
	case "no-terminal":
		return
	case "nonzero":
		fmt.Fprint(os.Stderr, "fixture credential "+os.Getenv("FIXTURE_API_KEY"))
		os.Exit(9)
	case "malformed":
		fmt.Println("this is not JSON")
		return
	case "failure":
		if codex {
			send(map[string]any{"type": "turn.failed", "error": map[string]any{"message": "fixture failure"}})
		} else {
			send(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true})
		}
		return
	case "huge":
		fmt.Println(strings.Repeat("x", 1024*1024))
		return
	case "stderr-flood":
		fmt.Fprint(os.Stderr, strings.Repeat("z", 2*1024*1024))
	case "hang", "child", "child-timeout":
		if mode != "hang" {
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), "WORKSHOP_FIXTURE_CHILD=1")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				os.Exit(12)
			}
			_ = os.WriteFile("child.pid", []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
		}
		for {
			time.Sleep(time.Second)
		}
	case "escape":
		_ = os.Symlink("/etc/passwd", "note.md")
	case "inner-link":
		_ = os.WriteFile("real.md", []byte("fixture artifact"), 0600)
		_ = os.Symlink("real.md", "note.md")
	}
	if mode != "escape" && mode != "inner-link" {
		_ = os.WriteFile("note.md", []byte("fixture artifact"), 0600)
	}
	text := "fixture answer"
	if mode == "secret" {
		text = os.Getenv("FIXTURE_API_KEY")
	}
	if codex {
		send(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": text}})
		send(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 12, "output_tokens": 4, "cached_input_tokens": 3}})
	} else {
		send(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
		session := fixtureSession
		if mode == "no-session" {
			session = ""
		}
		send(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": text, "session_id": session, "usage": map[string]any{"input_tokens": 12, "output_tokens": 4, "cache_read_input_tokens": 3}})
	}
	if mode == "terminal-nonzero" {
		os.Exit(9)
	}
}

func fixtureConfig(t *testing.T, engine string) Config {
	t.Helper()
	t.Setenv("WORKSHOP_FIXTURE", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Config{Root: t.TempDir(), Concurrency: 1, QueueCapacity: 1, Engines: map[string]EngineConfig{engine: {Binary: binary, EnvAllowlist: []string{"WORKSHOP_FIXTURE", "FIXTURE_API_KEY"}}}, Workflows: []Workflow{{Name: "note", Version: "v1", Instructions: "fixture instructions", Engine: engine, Model: "fixture-model", Policy: "workspace-write", TimeoutSeconds: 5, Artifacts: []string{"note.md"}}}}
}

func newFixtureService(t *testing.T, cfg Config) *Service {
	t.Helper()
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func waitTask(t *testing.T, s *Service, namespace, id string, want func(*Task) bool) *Task {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		task, err := s.Get(namespace, id)
		if err != nil {
			t.Fatal(err)
		}
		if want(task) {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, err := s.Get(namespace, id)
	t.Fatalf("task did not reach expected state: %+v, %v", task, err)
	return nil
}

func submit(t *testing.T, s *Service, input string) *Task {
	t.Helper()
	task, err := s.Submit(SubmitRequest{Namespace: "tenant-a", Workflow: "note", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestNativeChildSuccessResumeAndSnapshot(t *testing.T) {
	for _, engine := range []string{"codex", "claude"} {
		t.Run(engine, func(t *testing.T) {
			cfg := fixtureConfig(t, engine)
			t.Setenv("UNLISTED_SECRET", "must-not-inherit")
			s := newFixtureService(t, cfg)
			cfg.Workflows[0].Artifacts[0] = "mutated"
			task := submit(t, s, "success")
			task = waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return terminal(t.Status) })
			if task.Status != Succeeded {
				t.Fatalf("task: %+v", task)
			}
			run := task.Runs[0]
			if run.Text != "fixture answer" || run.Usage.InputTokens != 12 || run.Usage.OutputTokens != 4 || run.Usage.CachedInputTokens != 3 || task.SessionID != fixtureSession {
				t.Fatalf("unexpected result: %+v", run)
			}
			if len(run.Artifacts) != 1 || run.Artifacts[0].Path != "note.md" || run.Artifacts[0].Size != 16 || len(run.Artifacts[0].SHA256) != 64 {
				t.Fatalf("artifacts: %+v", run.Artifacts)
			}
			var capture struct {
				Args     []string `json:"args"`
				Stdin    string   `json:"stdin"`
				Cwd      string   `json:"cwd"`
				Unlisted string   `json:"unlisted_secret"`
			}
			data, err := os.ReadFile(filepath.Join(task.Workspace, "capture.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, &capture); err != nil {
				t.Fatal(err)
			}
			if capture.Cwd != task.Workspace || capture.Stdin != "fixture instructions\n\nUser input:\nsuccess" || capture.Unlisted != "" {
				t.Fatalf("capture: %+v", capture)
			}
			args := strings.Join(capture.Args, " ")
			if strings.Contains(args, "dangerous") || !strings.Contains(args, "fixture-model") {
				t.Fatal(args)
			}
			if engine == "codex" && (!strings.Contains(args, `sandbox_mode="workspace-write"`) || capture.Args[len(capture.Args)-1] != "-") {
				t.Fatal(args)
			}
			if engine == "claude" && (!strings.Contains(args, "--restricted") || !strings.Contains(args, "--permission-mode acceptEdits") || !strings.Contains(args, "--tools Read,Glob,Grep,Edit,Write")) {
				t.Fatal(args)
			}
			events, err := s.Events(task.Namespace, task.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) < 5 || events[0].Sequence != 1 || events[len(events)-1].Text != "succeeded" {
				t.Fatalf("events: %+v", events)
			}
			tail, err := s.Events(task.Namespace, task.ID, events[len(events)-2].Sequence)
			if err != nil || len(tail) != 1 {
				t.Fatalf("tail: %+v %v", tail, err)
			}
			workspace := task.Workspace
			if _, err = s.Resume(task.Namespace, task.ID, "followup"); err != nil {
				t.Fatal(err)
			}
			task = waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return terminal(t.Status) })
			if task.Status != Succeeded || len(task.Runs) != 2 || task.Workspace != workspace || task.Runs[0].Text != "fixture answer" || task.Runs[1].ResumeSessionID != fixtureSession {
				t.Fatalf("resume: %+v", task)
			}
			data, err = os.ReadFile(filepath.Join(task.Workspace, "capture.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, &capture); err != nil {
				t.Fatal(err)
			}
			args = strings.Join(capture.Args, " ")
			if !strings.Contains(args, fixtureSession) || !strings.Contains(args, "resume") || !strings.HasSuffix(capture.Stdin, "followup") {
				t.Fatalf("resume capture: %+v", capture)
			}
		})
	}
}

func TestNativeChildFailuresAndBounds(t *testing.T) {
	for _, engine := range []string{"codex", "claude"} {
		for _, mode := range []string{"no-terminal", "no-session", "nonzero", "terminal-nonzero", "failure", "malformed", "huge", "escape", "stderr-flood", "secret"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				cfg := fixtureConfig(t, engine)
				cfg.MaxOutputBytes = 4096
				t.Setenv("FIXTURE_API_KEY", "top-secret-fixture-key")
				s := newFixtureService(t, cfg)
				task := submit(t, s, mode)
				task = waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return terminal(t.Status) })
				want := Failed
				if mode == "stderr-flood" || mode == "secret" {
					want = Succeeded
				}
				if task.Status != want {
					t.Fatalf("want %s: %+v", want, task)
				}
				events, err := s.Events(task.Namespace, task.ID, 0)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(struct {
					Task   *Task
					Events []Event
				}{task, events})
				if strings.Contains(string(data), "top-secret-fixture-key") {
					t.Fatal("credential leaked")
				}
				if len(data) > 30000 {
					t.Fatalf("unbounded stored output: %d", len(data))
				}
				if mode == "secret" && task.Runs[0].Text != "[REDACTED]" {
					t.Fatal(task.Runs[0].Text)
				}
			})
		}
	}
}

func TestNativeChildCancelDescendantsAndTimeout(t *testing.T) {
	for _, mode := range []string{"child", "child-timeout"} {
		t.Run(mode, func(t *testing.T) {
			cfg := fixtureConfig(t, "codex")
			if mode == "child-timeout" {
				cfg.Workflows[0].TimeoutSeconds = 1
			}
			s := newFixtureService(t, cfg)
			task := submit(t, s, mode)
			task = waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return t.SessionID != "" })
			pid := 0
			if mode == "child" || mode == "child-timeout" {
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					data, err := os.ReadFile(filepath.Join(task.Workspace, "child.pid"))
					if err == nil {
						pid, _ = strconv.Atoi(string(data))
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if pid == 0 {
					t.Fatal("fixture child not started")
				}
				if mode == "child" {
					if _, err := s.Cancel(task.Namespace, task.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			task = waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return terminal(t.Status) })
			want := Cancelled
			if mode == "child-timeout" {
				want = TimedOut
			}
			if task.Status != want {
				t.Fatalf("task: %+v", task)
			}
			if pid != 0 {
				deadline := time.Now().Add(time.Second)
				stopped := false
				for time.Now().Before(deadline) {
					data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
					if os.IsNotExist(err) || strings.Contains(string(data), ") Z") {
						stopped = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !stopped {
					t.Fatalf("descendant %d still running", pid)
				}
			}
		})
	}
}

func TestNamespacesIdempotencyAndPersistence(t *testing.T) {
	cfg := fixtureConfig(t, "codex")
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req := SubmitRequest{Namespace: "a", Workflow: "note", Input: "success", IdempotencyKey: "key"}
	task, err := s.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Submit(req)
	if err != nil || again.ID != task.ID {
		t.Fatalf("idempotency: %+v %v", again, err)
	}
	req.Input = "changed"
	if _, err = s.Submit(req); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.Get("b", task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = s.Cancel("b", task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = s.Resume("b", task.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = s.Events("b", task.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	list, err := s.List("b")
	if err != nil || len(list) != 0 {
		t.Fatalf("namespace list: %+v %v", list, err)
	}
	task = waitTask(t, s, "a", task.ID, func(t *Task) bool { return terminal(t.Status) })
	req.Namespace = "b"
	other, err := s.Submit(req)
	if err != nil || other.ID == task.ID {
		t.Fatalf("scoped key: %+v %v", other, err)
	}
	if _, err = New(cfg, nil); err == nil {
		t.Fatal("second service acquired live database lock")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Workflows[0].Instructions = "new instructions"
	cfg.Workflows[0].Version = "v2"
	s, err = New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req.Namespace = "a"
	req.Input = "success"
	again, err = s.Submit(req)
	if err != nil || again.ID != task.ID || again.Workflow.Version != "v1" || again.Workflow.Instructions != "fixture instructions" {
		t.Fatalf("persisted snapshot: %+v %v", again, err)
	}
}

func TestRecoveryDoesNotReplay(t *testing.T) {
	cfg := fixtureConfig(t, "codex")
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	task := submit(t, s, "hang")
	waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return t.SessionID != "" })
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate an abrupt previous process exit by restoring its durable running
	// state after releasing the real service lock. New must not enqueue it.
	db, err := bolt.Open(filepath.Join(cfg.Root, "workshop.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		stored, err := readTask(tx, task.Namespace, task.ID)
		if err != nil {
			return err
		}
		stored.Status = Running
		stored.Runs[0].Status = Running
		stored.Runs[0].FinishedAt = nil
		return putTask(tx, stored)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s = newFixtureService(t, cfg)
	recovered, err := s.Get(task.Namespace, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != Interrupted || len(recovered.Runs) != 1 || recovered.SessionID != fixtureSession || recovered.Runs[0].FinishedAt == nil {
		t.Fatalf("recovery: %+v", recovered)
	}
	if len(s.queue) != 0 {
		t.Fatal("recovered task was automatically queued")
	}
	if _, err = s.Resume(task.Namespace, task.ID, "success"); err != nil {
		t.Fatal(err)
	}
	recovered = waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return terminal(t.Status) })
	if recovered.Status != Succeeded || len(recovered.Runs) != 2 {
		t.Fatalf("recovery resume: %+v", recovered)
	}
}

type blockingRunner struct {
	mu          sync.Mutex
	active, max int
	started     chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, in Invocation, emit func(Event) error) (Result, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.max {
		r.max = r.active
	}
	r.mu.Unlock()
	r.started <- struct{}{}
	<-ctx.Done()
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return Result{}, ctx.Err()
}

func TestQueueBoundAndConcurrency(t *testing.T) {
	cfg := fixtureConfig(t, "codex")
	cfg.Concurrency = 2
	cfg.QueueCapacity = 1
	runner := &blockingRunner{started: make(chan struct{}, 3)}
	s, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := submit(t, s, "a")
	b := submit(t, s, "b")
	for i := 0; i < 2; i++ {
		select {
		case <-runner.started:
		case <-time.After(time.Second):
			t.Fatal("worker not started")
		}
	}
	c := submit(t, s, "c")
	if _, err = s.Submit(SubmitRequest{Namespace: "tenant-a", Workflow: "note", Input: "d"}); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	if _, err = s.Cancel(c.Namespace, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cancel(a.Namespace, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cancel(b.Namespace, b.ID); err != nil {
		t.Fatal(err)
	}
	for _, task := range []*Task{a, b, c} {
		waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return terminal(t.Status) })
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.max != 2 {
		t.Fatalf("max concurrency %d", runner.max)
	}
}

func TestArtifactsRejectTraversalAndSymlink(t *testing.T) {
	for _, path := range []string{"../escape", "/etc/passwd", "a/../../escape", "a/../file", ".", "a\x00b"} {
		t.Run(path, func(t *testing.T) {
			cfg := fixtureConfig(t, "codex")
			cfg.Workflows[0].Artifacts = []string{path}
			if s, err := New(cfg, nil); err == nil {
				s.Close()
				t.Fatal("invalid artifact accepted")
			}
		})
	}
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := collectArtifacts(workspace, []string{"out/secret"}); err == nil {
		t.Fatal("symlink escape accepted")
	}
	if _, err := collectArtifacts(workspace, []string{"missing"}); err == nil {
		t.Fatal("missing artifact accepted")
	}
	if err := syscall.Mkfifo(filepath.Join(workspace, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := collectArtifacts(workspace, []string{"fifo"}); finished <- err }()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("FIFO artifact accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("artifact reader blocked on FIFO")
	}
}

// A symlink to another workspace file must fail collection: downloads refuse
// every symlink, so accepting it would record an artifact that can never be read.
func TestArtifactSymlinkInsideWorkspaceFailsCollection(t *testing.T) {
	s := newFixtureService(t, fixtureConfig(t, "codex"))
	task := submit(t, s, "inner-link")
	task = waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return terminal(t.Status) })
	if task.Status != Failed || len(task.Runs[0].Artifacts) != 0 {
		t.Fatalf("in-workspace symlink artifact accepted: %+v", task)
	}
	if _, err := os.Lstat(filepath.Join(task.Workspace, "real.md")); err != nil {
		t.Fatalf("fixture did not create the link target: %v", err)
	}
}

// Service-level successor of the removed HTTP transport test: unknown workflow,
// missing tasks, then a submit/list/get/events/resume/cancel cycle.
func TestServiceOperatorLifecycle(t *testing.T) {
	s := newFixtureService(t, fixtureConfig(t, "codex"))
	if _, err := s.Submit(SubmitRequest{Namespace: "operator", Workflow: "unknown", Input: "x"}); !errors.Is(err, ErrWorkflow) {
		t.Fatalf("unknown workflow: %v", err)
	}
	if _, err := s.Get("operator", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	if _, err := s.Cancel("operator", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel missing: %v", err)
	}
	if _, err := s.Resume("operator", "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resume missing: %v", err)
	}
	task, err := s.Submit(SubmitRequest{Namespace: "operator", Workflow: "note", Input: "success", IdempotencyKey: "http"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Namespace != "operator" {
		t.Fatal(task.Namespace)
	}
	waitTask(t, s, "operator", task.ID, func(t *Task) bool { return terminal(t.Status) })
	if tasks, err := s.List("operator"); err != nil || len(tasks) != 1 {
		t.Fatalf("list: %d %v", len(tasks), err)
	}
	if _, err := s.Get("operator", task.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := s.Events("operator", task.ID, 0); err != nil {
		t.Fatalf("events: %v", err)
	}
	if _, err := s.Resume("operator", task.ID, "followup"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := s.Cancel("operator", task.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

func TestWorkflowCatalog(t *testing.T) {
	cfg := fixtureConfig(t, "codex")
	second := cfg.Workflows[0]
	second.Name = "another-note"
	second.Artifacts = nil
	cfg.Workflows = append(cfg.Workflows, second)
	s := newFixtureService(t, cfg)
	catalog := s.Workflows()
	if len(catalog) != 2 || catalog[0].Name != "another-note" || catalog[1].Name != "note" {
		t.Fatalf("catalog: %+v", catalog)
	}
	catalog[1].Name = "changed"
	catalog[1].Artifacts[0] = "changed.md"
	if current := s.Workflows(); current[1].Name != "note" || current[1].Artifacts[0] != "note.md" {
		t.Fatalf("catalog mutation affected service: %+v", current)
	}
	raw, err := json.Marshal(s.Workflows())
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	expected := []map[string]any{
		{"name": "another-note", "version": "v1", "engine": "codex", "model": "fixture-model", "policy": "workspace-write", "timeout_seconds": float64(5), "artifacts": []any{}},
		{"name": "note", "version": "v1", "engine": "codex", "model": "fixture-model", "policy": "workspace-write", "timeout_seconds": float64(5), "artifacts": []any{"note.md"}},
	}
	if !reflect.DeepEqual(records, expected) {
		t.Fatalf("unexpected or private catalog fields: %s", raw)
	}
	// Discovery must not change execution or the immutable workflow snapshot.
	task := submit(t, s, "success")
	task = waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return terminal(t.Status) })
	if task.Status != Succeeded || task.Workflow.Instructions != "fixture instructions" || task.Workflow.Artifacts[0] != "note.md" {
		t.Fatalf("catalog mutation affected task: %+v", task)
	}
}

func TestReadOnlyArguments(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		cfg := fixtureConfig(t, engine)
		w := cfg.Workflows[0]
		w.Policy = "read-only"
		args, err := engineArgs(Invocation{Workflow: w}, string(w.Policy))
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, " ")
		if engine == "claude" && (strings.Contains(joined, "Edit") || !strings.Contains(joined, "dontAsk")) {
			t.Fatal(args)
		}
		if engine == "codex" && !strings.Contains(joined, `sandbox_mode="read-only"`) {
			t.Fatal(args)
		}
	}
}

// A task is reachable only through the namespace it was submitted in.
func TestServiceNamespaceIsolation(t *testing.T) {
	s := newFixtureService(t, fixtureConfig(t, "codex"))
	task, err := s.Submit(SubmitRequest{Namespace: "private-session", Workflow: "note", Input: "success", IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Namespace != "private-session" {
		t.Fatal(task.Namespace)
	}
	for _, c := range []struct {
		name string
		call func() error
	}{
		{"get", func() error { _, err := s.Get("operator", task.ID); return err }},
		{"events", func() error { _, err := s.Events("operator", task.ID, 0); return err }},
		{"cancel", func() error { _, err := s.Cancel("operator", task.ID); return err }},
		{"resume", func() error { _, err := s.Resume("operator", task.ID, "followup"); return err }},
	} {
		if err := c.call(); !errors.Is(err, ErrNotFound) {
			t.Fatalf("wrong namespace %s: %v", c.name, err)
		}
	}
	if _, err := s.Get("private-session", task.ID); err != nil {
		t.Fatalf("scoped get: %v", err)
	}
}

func TestRunnerPropagatesDurableEventFailure(t *testing.T) {
	cfg := fixtureConfig(t, "codex")
	r, err := NewCommandRunner(cfg.Engines, 0)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("storage failed")
	_, err = r.Run(context.Background(), Invocation{Workflow: cfg.Workflows[0], Workspace: t.TempDir(), Input: "hang"}, func(Event) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("emit error: %v", err)
	}
}

func TestTaskReadsAreCopies(t *testing.T) {
	s := newFixtureService(t, fixtureConfig(t, "codex"))
	task := submit(t, s, "success")
	task = waitTask(t, s, task.Namespace, task.ID, func(t *Task) bool { return terminal(t.Status) })
	before := task.Workflow
	task.Workflow.Artifacts[0] = "changed"
	got, err := s.Get(task.Namespace, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, got.Workflow) {
		t.Fatal("test did not mutate its copy")
	}
	if got.Workflow.Artifacts[0] != "note.md" {
		t.Fatal("read mutated persisted task")
	}
}
