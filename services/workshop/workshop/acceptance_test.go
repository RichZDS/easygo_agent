//go:build linux

package workshop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func TestAcceptanceConfig(t *testing.T) {
	valid := &AcceptanceConfig{Checks: []AcceptanceCheck{{Name: "unit", Command: []string{"/pack/checks/unit", "arg"}, TimeoutSeconds: 1}}}
	if err := validateAcceptance(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*AcceptanceConfig){
		func(c *AcceptanceConfig) { c.Checks = nil }, func(c *AcceptanceConfig) { c.Checks = append(c.Checks, c.Checks[0]) },
		func(c *AcceptanceConfig) { c.Checks[0].Name = "Upper" }, func(c *AcceptanceConfig) { c.Checks[0].Command = []string{"relative"} },
		func(c *AcceptanceConfig) { c.Checks[0].TimeoutSeconds = 0 }, func(c *AcceptanceConfig) { c.Checks[0].TimeoutSeconds = 1801 },
		func(c *AcceptanceConfig) { c.Checks[0].Command = []string{"/bin/x", "nul\x00"} },
	} {
		cfg := cloneAcceptance(valid)
		mutate(cfg)
		if !errors.Is(validateAcceptance(cfg), ErrInvalid) {
			t.Fatal("invalid acceptance accepted")
		}
	}
	cfg := fixtureConfig(t, "codex")
	cfg.Workflows[0].Acceptance = valid
	if s, err := New(cfg, nil); !errors.Is(err, ErrInvalid) {
		if s != nil {
			s.Close()
		}
		t.Fatal("host accepted checks", err)
	}
	if _, err := snapshotChecks(t.TempDir(), "", true); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing pack accepted", err)
	}
	in := Invocation{Workflow: Workflow{Instructions: "work", Acceptance: valid}, Input: "input", WorkerInstructions: "worker"}
	if invocationPrompt(in) != "worker\n\nwork\n\nAcceptance checks the platform will run after you finish:\n- unit: /pack/checks/unit arg\n\nUser input:\ninput" {
		t.Fatal("check prompt missing")
	}
}
func TestPackSnapshotAndWorkspaceHash(t *testing.T) {
	pack, root := t.TempDir(), t.TempDir()
	t.Cleanup(func() { writableTree(root) })
	checks := filepath.Join(pack, "checks")
	if err := os.Mkdir(checks, 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(checks, "unit.sh")
	if err := os.WriteFile(script, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotChecks(root, pack, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySealedChecks(filepath.Dir(snapshot)); err != nil {
		t.Fatal(err)
	}
	if again, err := snapshotChecks(root, pack, true); err != nil || again != snapshot {
		t.Fatal("snapshot not reused", err)
	}
	if err := os.WriteFile(script, []byte("changed"), 0755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(snapshot, "unit.sh"))
	if err != nil || string(raw) != "original" {
		t.Fatal("snapshot not immutable", err)
	}
	updated, err := snapshotChecks(root, pack, true)
	if err != nil || updated == snapshot {
		t.Fatal("pack hash missed content change", err)
	}
	if err := os.Symlink("unit.sh", filepath.Join(checks, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotChecks(root, pack, true); err == nil {
		t.Fatal("pack symlink copied")
	}
	workspace := t.TempDir()
	file := filepath.Join(workspace, "a")
	if err := os.WriteFile(file, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := workspaceTreeHash(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(workspace, ".workshop-home")
	os.Mkdir(home, 0700)
	os.WriteFile(filepath.Join(home, "state"), []byte("ignored"), 0600)
	if hash, err := workspaceTreeHash(context.Background(), workspace); err != nil || hash != first {
		t.Fatal("native home affected tree hash", err)
	}
	os.WriteFile(file, []byte("two"), 0600)
	second, err := workspaceTreeHash(context.Background(), workspace)
	if err != nil || second == first {
		t.Fatal("content not hashed", err)
	}
	os.Rename(file, filepath.Join(workspace, "b"))
	third, err := workspaceTreeHash(context.Background(), workspace)
	if err != nil || third == second {
		t.Fatal("path not hashed", err)
	}
	os.Remove(filepath.Join(workspace, "b"))
	os.Symlink("two", filepath.Join(workspace, "b"))
	fourth, err := workspaceTreeHash(context.Background(), workspace)
	if err != nil || fourth == third {
		t.Fatal("type not hashed", err)
	}
}
func TestEvidenceTruncationAndFalseGreen(t *testing.T) {
	var raw bytes.Buffer
	writer := &evidenceWriter{file: &raw}
	input := bytes.Repeat([]byte("x"), evidenceLimit+100)
	if n, err := writer.Write(input); err != nil || n != len(input) || raw.Len() != evidenceLimit || writer.total != int64(len(input)) {
		t.Fatal("evidence not bounded")
	}
	for _, outcome := range []string{"submitted", "asked", "blocked", "none"} {
		for _, tests := range []string{"pass", "fail", "not_run", ""} {
			for _, state := range []string{"passed", "failed", "error", "cancelled", "interrupted", "skipped"} {
				want := outcome == "submitted" && tests == "pass" && state == "failed"
				if falseGreen(outcome, tests, state) != want {
					t.Fatal(outcome, tests, state)
				}
			}
		}
	}
}
func TestEvidencePaginationAndScope(t *testing.T) {
	s, task, _, release, _ := crewFixture(t)
	release <- struct{}{}
	task = waitTask(t, s, "owner", task.ID, func(task *Task) bool { return terminal(task.Status) })
	eid := uuid.NewString()
	dir := filepath.Join(s.root, "evidence", task.ID)
	os.MkdirAll(dir, 0700)
	text := "a界bc界z"
	os.WriteFile(filepath.Join(dir, eid+".log"), []byte(text), 0600)
	s.mu.Lock()
	err := s.db.Update(func(tx *bolt.Tx) error {
		task.Runs[0].Acceptance = &Acceptance{State: "failed", FalseGreen: true, Evidence: []Evidence{{ID: eid, Check: "unit", OutputBytes: int64(len(text))}}}
		return putTask(tx, task)
	})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Evidence("owner", task.ID, "", "", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	list := result.(EvidenceList)
	if list.Namespace != "owner" || list.TaskID != task.ID || list.RunID != task.Runs[0].ID || len(list.Evidence) != 1 || !list.FalseGreen {
		t.Fatal(list)
	}
	var rebuilt strings.Builder
	for offset := 0; ; {
		result, err := s.Evidence("owner", task.ID, task.Runs[0].ID, eid, offset, 4)
		if err != nil {
			t.Fatal(err)
		}
		page := result.(EvidencePage)
		if page.Namespace != "owner" || page.TaskID != task.ID || page.RunID != task.Runs[0].ID || page.EvidenceID != eid || page.Offset != offset {
			t.Fatal("page scope")
		}
		rebuilt.WriteString(page.Text)
		offset = page.NextOffset
		if page.EOF {
			break
		}
	}
	if rebuilt.String() != text {
		t.Fatal("pagination lost UTF-8")
	}
	if _, err := s.Evidence("other", task.ID, "", eid, 0, 4); !errors.Is(err, ErrNotFound) {
		t.Fatal("scope leak", err)
	}
	if _, err := s.Evidence("owner", task.ID, "", eid, 2, 4); !errors.Is(err, ErrInvalid) {
		t.Fatal("split rune accepted", err)
	}
	if _, err := s.Evidence("owner", task.ID, "missing", eid, 0, 4); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Evidence("owner", task.ID, "", "missing", 0, 4); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, eid+".log"), []byte{0x80, 0x80, 'a'}, 0600)
	if value, err := s.Evidence("owner", task.ID, "", eid, 0, 4); err != nil || !value.(EvidencePage).EOF {
		t.Fatal("binary output inaccessible", err)
	}
}
func TestAcceptanceRestartInterruption(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Concurrency: 1, Workflows: []Workflow{{Name: "test", Version: "1", Engine: "codex", Model: "fixture", Instructions: "offline", Policy: "workspace-write", TimeoutSeconds: 5}}}
	s, err := New(cfg, artifactRunnerFunc(func(context.Context, Invocation, func(Event) error) (Result, error) { return Result{}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	task := Task{ID: id, Namespace: "owner", Status: Running, Workflow: Workflow{Acceptance: &AcceptanceConfig{Checks: []AcceptanceCheck{{Name: "unit", Command: []string{"/unit"}, TimeoutSeconds: 1}}}}, Runs: []Run{{ID: uuid.NewString(), Status: Running, Acceptance: &Acceptance{State: "running", Evidence: []Evidence{}}}}}
	// Persist a crash image directly, then close only the database. No worker was
	// started for this seeded task, so orderly Service.Close cannot finalize it.
	s.mu.Lock()
	err = s.db.Update(func(tx *bolt.Tx) error { return putTask(tx, &task) })
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.closed = true
	close(s.stop)
	s.mu.Unlock()
	s.wg.Wait()
	s.db.Close()
	restored, err := New(cfg, artifactRunnerFunc(func(context.Context, Invocation, func(Event) error) (Result, error) { return Result{}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := restored.Get("owner", id)
	if err != nil || got.Status != Interrupted || got.Runs[0].Acceptance.State != "interrupted" {
		t.Fatal(got, err)
	}
	events, err := restored.Events("owner", id, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Acceptance != nil && event.Acceptance.State == "interrupted" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing acceptance interruption event")
	}
}

// TestAcceptanceRestartRecomputesFalseGreen formalizes the terminal review's
// probe: a crashed run stored as acceptance failed + false_green=true is
// forced to interrupted on restart, and must not keep a false_green flag
// that the truth table (submitted + tests=pass + failed) no longer supports
// once the state is interrupted.
func TestAcceptanceRestartRecomputesFalseGreen(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Root: root, Concurrency: 1, Workflows: []Workflow{{Name: "test", Version: "1", Engine: "codex", Model: "fixture", Instructions: "offline", Policy: "workspace-write", TimeoutSeconds: 5}}}
	s, err := New(cfg, artifactRunnerFunc(func(context.Context, Invocation, func(Event) error) (Result, error) { return Result{}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	id, runID := uuid.NewString(), uuid.NewString()
	task := Task{ID: id, Namespace: "owner", Status: Running, Workflow: Workflow{Acceptance: &AcceptanceConfig{Checks: []AcceptanceCheck{{Name: "unit", Command: []string{"/unit"}, TimeoutSeconds: 1}}}}, Runs: []Run{{ID: runID, Status: Running, Acceptance: &Acceptance{State: "failed", FalseGreen: true, Evidence: []Evidence{}}}}}
	s.mu.Lock()
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := appendEvent(tx, &task, Event{Kind: "crew.message", Message: &CrewMessage{ID: uuid.NewString(), Direction: "from_worker", Kind: "submit", Text: "ready", Claims: &CrewClaims{Tests: "pass"}}}); err != nil {
			return err
		}
		return putTask(tx, &task)
	})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.closed = true
	close(s.stop)
	s.mu.Unlock()
	s.wg.Wait()
	s.db.Close()
	restored, err := New(cfg, artifactRunnerFunc(func(context.Context, Invocation, func(Event) error) (Result, error) { return Result{}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := restored.Get("owner", id)
	if err != nil || got.Status != Interrupted || got.Runs[0].Acceptance.State != "interrupted" || got.Runs[0].Acceptance.FalseGreen {
		t.Fatalf("got=%+v acceptance=%+v err=%v", got, got.Runs[0].Acceptance, err)
	}
	events, err := restored.Events("owner", id, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Acceptance != nil && event.Acceptance.State == "interrupted" {
			found = true
			if event.Acceptance.FalseGreen {
				t.Fatal("interruption event still carries the stale false_green")
			}
		}
	}
	if !found {
		t.Fatal("missing acceptance interruption event")
	}
}
func TestAcceptanceContainerOptions(t *testing.T) {
	r, _, _ := dockerFixture(t)
	args := r.checkContainerOptions("check", "/host/work", "/host/checks", AcceptanceCheck{Command: []string{"/pack/checks/unit", "arg"}})
	for k, v := range map[string]string{"--entrypoint": "/pack/checks/unit", "--network": "none", "--user": "1000:1000", "--cap-drop": "ALL", "--security-opt": "no-new-privileges=true"} {
		if option(args, k) != v {
			t.Fatal(k)
		}
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "EASYGO_") || strings.Contains(joined, "relay") {
		t.Fatal("check isolation arguments")
	}
	// Assert the workspace and pack mounts are readonly by field-set
	// membership, not by locking the whole mount string's field order.
	if mount, ok := findMount(args, runtimeWorkspace); !ok || !slices.Contains(mount, "readonly") {
		t.Fatalf("check workspace mount not readonly: %v", mount)
	}
	if mount, ok := findMount(args, "/pack/checks"); !ok || !slices.Contains(mount, "readonly") {
		t.Fatalf("check pack mount not readonly: %v", mount)
	}
}
