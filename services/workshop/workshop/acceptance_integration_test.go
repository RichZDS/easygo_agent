package workshop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"easygo-agent/rpc"
	"github.com/google/uuid"
)

func TestAcceptanceDockerIntegration(t *testing.T) {
	endpoint, image := os.Getenv("EASYGO_DOCKER_TEST_ENDPOINT"), os.Getenv("EASYGO_DOCKER_TEST_IMAGE")
	if endpoint == "" || image == "" {
		t.Skip("requires explicit dedicated Docker endpoint and fixture image")
	}
	root, err := os.MkdirTemp("/tmp", "accept-proof-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { writableTree(root); os.RemoveAll(root) }()
	pack := filepath.Join(root, "source-pack")
	os.MkdirAll(filepath.Join(pack, "checks"), 0700)
	source := filepath.Join(pack, "checks", "source.txt")
	if err := os.WriteFile(source, []byte("trusted-original"), 0600); err != nil {
		t.Fatal(err)
	}
	gateway := nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
	check := func(name string, seconds int, args ...string) AcceptanceCheck {
		return AcceptanceCheck{Name: name, Command: append([]string{"/usr/local/bin/fixture-check"}, args...), TimeoutSeconds: seconds}
	}
	definitions := map[string][]AcceptanceCheck{
		"pass":           {check("artifact", 5, "file", "/workspace/artifact.txt", "good"), check("pack", 5, "file", "/pack/checks/source.txt", "trusted-original"), check("isolation", 5, "isolation")},
		"bad":            {check("artifact", 5, "file", "/workspace/artifact.txt", "good")},
		"timeout":        {check("timeout", 1, "sleep", "5")},
		"cancel":         {check("cancel", 30, "sleep", "60")},
		"output":         {check("output", 5, "output", "1048600")},
		"infrastructure": {{Name: "missing", Command: []string{"/missing-check"}, TimeoutSeconds: 5}},
		"skip":           {check("never", 5, "fail")},
	}
	workflows := []Workflow{}
	for name, checks := range definitions {
		workflows = append(workflows, Workflow{Name: name, Version: "1", Runtime: "fixture", Instructions: "offline", Policy: "workspace-write", TimeoutSeconds: 60, Artifacts: []string{"artifact.txt"}, Acceptance: &AcceptanceConfig{Checks: checks}})
	}
	cfg := Config{Root: root, PackDir: pack, Concurrency: 1, QueueCapacity: 1, ModelGateway: gateway, Engines: map[string]EngineConfig{"codex": {}}, RuntimeProfiles: map[string]RuntimeProfile{"fixture": {Engine: "codex", Protocol: "responses", GatewayModel: "fixture"}}, Workflows: workflows, Sandbox: SandboxConfig{Mode: "docker", DockerBinary: os.Getenv("EASYGO_DOCKER_TEST_BINARY"), Endpoint: endpoint, Image: image, Owner: "accept-proof-" + uuid.NewString(), HostRoot: root}}
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if strings.HasPrefix(s.packChecks, filepath.Join(root, "workspaces")+"/") {
		t.Fatal("checks copied into worker workspace")
	}
	if err := os.WriteFile(source, []byte("operator-source-changed"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pass", "bad", "timeout", "cancel", "output", "infrastructure", "skip"} {
		t.Run(name, func(t *testing.T) {
			content := "good"
			if name == "bad" {
				content = "bad"
			}
			script := []map[string]any{
				{"op": "write-denied", "path": "/pack/checks/source.txt"},
				{"op": "write-denied", "path": filepath.Join(s.packChecks, "source.txt")},
				{"op": "write", "path": "artifact.txt", "text": content},
				{"op": "crew", "args": []string{"submit", "--tests", "pass", "ready"}},
			}
			if name == "skip" {
				script = append(script, map[string]any{"op": "exit", "exit_code": 9})
			}
			raw, _ := json.Marshal(map[string]any{"mode": "crew", "script": script})
			task, err := s.Submit(SubmitRequest{Namespace: "task-namespace", Workflow: name, Input: string(raw)})
			if err != nil {
				t.Fatal(err)
			}
			if name == "cancel" {
				deadline := time.Now().Add(15 * time.Second)
				started := false
				for time.Now().Before(deadline) {
					files, _ := filepath.Glob(filepath.Join(root, "evidence", task.ID, "*.log"))
					for _, file := range files {
						body, _ := os.ReadFile(file)
						if strings.Contains(string(body), "sleeping check") {
							started = true
						}
					}
					if started {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if !started {
					t.Fatal("check did not actually start")
				}
				if _, err := s.Cancel(task.Namespace, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			task = waitTask(t, s, task.Namespace, task.ID, func(task *Task) bool { return terminal(task.Status) })
			run := task.Runs[0]
			wantStatus := Succeeded
			wantState := map[string]string{"pass": "passed", "bad": "failed", "timeout": "failed", "cancel": "cancelled", "output": "passed", "infrastructure": "error", "skip": "skipped"}[name]
			if name == "cancel" {
				wantStatus = Cancelled
			}
			if name == "skip" {
				wantStatus = Failed
			}
			if task.Status != wantStatus || run.Acceptance == nil || run.Acceptance.State != wantState {
				t.Fatalf("status=%s acceptance=%+v error=%s", task.Status, run.Acceptance, run.Error)
			}
			if (name == "bad" || name == "timeout") != run.Acceptance.FalseGreen {
				t.Fatal("false green mismatch", run.Acceptance)
			}
			if name == "cancel" && len(run.Artifacts) != 0 {
				t.Fatal("cancelled check published artifact")
			}
			if name == "timeout" && (len(run.Acceptance.Evidence) != 1 || !run.Acceptance.Evidence[0].TimedOut) {
				t.Fatal("timeout evidence missing")
			}
			if name == "output" && (len(run.Acceptance.Evidence) != 1 || !run.Acceptance.Evidence[0].OutputTruncated || run.Acceptance.Evidence[0].OutputBytes != 1048600) {
				t.Fatal("truncation evidence missing", run.Acceptance)
			}
			for _, evidence := range run.Acceptance.Evidence {
				if len(evidence.WorkspaceSHA256) != 64 {
					t.Fatal("workspace hash missing")
				}
				result, err := s.Evidence(task.Namespace, task.ID, run.ID, evidence.ID, 0, 32768)
				if err != nil {
					t.Fatal(err)
				}
				page := result.(EvidencePage)
				if page.TotalBytes > evidenceLimit {
					t.Fatal("output limit exceeded")
				}
				if evidence.Check == "isolation" && (!strings.Contains(page.Text, "network denied: true") || !strings.Contains(page.Text, "write denied /workspace/check-write: true") || !strings.Contains(page.Text, "write denied /pack/checks/check-write: true") || !strings.Contains(page.Text, "no credentials or relay: true")) {
					t.Fatal("isolation evidence missing", page.Text)
				}
				t.Logf("%s: exit=%d timed_out=%t output_bytes=%d truncated=%t hash=%s output=%s", evidence.Check, evidence.ExitCode, evidence.TimedOut, evidence.OutputBytes, evidence.OutputTruncated, evidence.WorkspaceSHA256, prefix(page.Text, 250))
			}
			summary, err := s.Summary(task.Namespace, task.ID)
			if err != nil || summary.Runs[0].AcceptanceState != wantState || summary.Runs[0].EvidenceCount != len(run.Acceptance.Evidence) {
				t.Fatal("summary missing acceptance", err)
			}
			docker := s.runner.(*DockerRunner)
			ids, err := docker.output(context.Background(), "ps", "-aq", "--filter", "label="+ownerLabel+"="+cfg.Sandbox.Owner)
			if err != nil || ids != "" {
				t.Fatal("check/task container leaked", err)
			}
			t.Logf("task=%s acceptance=%s false_green=%t evidence=%d; owned containers=0", task.Status, run.Acceptance.State, run.Acceptance.FalseGreen, len(run.Acceptance.Evidence))
		})
	}
}

// TestAcceptanceDockerCleanupResilienceRealContainer wraps a real Docker
// command so the check container's rm fails exactly 3 times, proving against
// a real daemon (not just the fake) that: the check still reports passed with
// a duration excluding recycling; the failure never latches cleanupFailure;
// and the next task's admission sweep actually removes the stopped container.
func TestAcceptanceDockerCleanupResilienceRealContainer(t *testing.T) {
	endpoint, image := os.Getenv("EASYGO_DOCKER_TEST_ENDPOINT"), os.Getenv("EASYGO_DOCKER_TEST_IMAGE")
	if endpoint == "" || image == "" {
		t.Skip("requires explicit dedicated Docker endpoint and fixture image")
	}
	root, err := os.MkdirTemp("/tmp", "cleanup-resilience-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { writableTree(root); os.RemoveAll(root) }()
	pack := filepath.Join(root, "source-pack")
	if err := os.MkdirAll(filepath.Join(pack, "checks"), 0700); err != nil {
		t.Fatal(err)
	}
	gateway := nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
	check := AcceptanceCheck{Name: "artifact", Command: []string{"/usr/local/bin/fixture-check", "file", "/workspace/artifact.txt", "good"}, TimeoutSeconds: 5}
	owner := "cleanup-resilience-" + uuid.NewString()
	// Backstop: this test deliberately makes cleanup fail its first attempts,
	// so a bug (or a t.Fatal partway through) can leave a stopped container
	// behind. t.Cleanup runs after the test body's own defers (including
	// s.Close()), so it catches whatever those left, by owner label alone -
	// independent of whether the DockerRunner itself is still usable.
	t.Cleanup(func() {
		binary := os.Getenv("EASYGO_DOCKER_TEST_BINARY")
		if binary == "" {
			binary = "docker"
		}
		ids, _ := exec.Command(binary, "--host", endpoint, "ps", "-aq", "--filter", "label="+ownerLabel+"="+owner).Output()
		for _, id := range strings.Fields(string(ids)) {
			if err := exec.Command(binary, "--host", endpoint, "rm", "--force", id).Run(); err != nil {
				t.Logf("cleanup backstop: failed to remove leftover container %s: %v", id, err)
			}
		}
	})
	workflow := Workflow{Name: "quick", Version: "1", Runtime: "fixture", Instructions: "offline", Policy: "workspace-write", TimeoutSeconds: 60, Artifacts: []string{"artifact.txt"}, Acceptance: &AcceptanceConfig{Checks: []AcceptanceCheck{check}}}
	cfg := Config{Root: root, PackDir: pack, Concurrency: 1, QueueCapacity: 1, ModelGateway: gateway, Engines: map[string]EngineConfig{"codex": {}}, RuntimeProfiles: map[string]RuntimeProfile{"fixture": {Engine: "codex", Protocol: "responses", GatewayModel: "fixture"}}, Workflows: []Workflow{workflow}, Sandbox: SandboxConfig{Mode: "docker", DockerBinary: os.Getenv("EASYGO_DOCKER_TEST_BINARY"), Endpoint: endpoint, Image: image, Owner: owner, HostRoot: root}}
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	docker := s.runner.(*DockerRunner)
	docker.cleanupRetryBackoff = [2]time.Duration{50 * time.Millisecond, 50 * time.Millisecond}

	// Intercept only the first check container's rm: fail exactly the first 3
	// real attempts (matching cleanup's bounded retry), globally, so a later
	// task's own check container is never touched once that budget is spent.
	var mu sync.Mutex
	names := map[string]string{}
	injected := 0
	real := docker.command
	docker.command = func(ctx context.Context, in io.Reader, out, diag io.Writer, args ...string) error {
		if len(args) > 0 && args[0] == "create" {
			name := option(args, "--name")
			rec := &bytes.Buffer{}
			err := real(ctx, in, io.MultiWriter(out, rec), diag, args...)
			if err == nil && name != "" {
				mu.Lock()
				names[strings.TrimSpace(rec.String())] = name
				mu.Unlock()
			}
			return err
		}
		if len(args) > 0 && args[0] == "rm" {
			id := args[len(args)-1]
			mu.Lock()
			name := names[id]
			inject := strings.HasPrefix(name, "easygo-check-") && injected < 3
			if inject {
				injected++
			}
			mu.Unlock()
			if inject {
				return errors.New("injected rm failure for cleanup-resilience test")
			}
		}
		return real(ctx, in, out, diag, args...)
	}

	submit := func() *Task {
		raw, _ := json.Marshal(map[string]any{"mode": "crew", "script": []map[string]any{
			{"op": "write", "path": "artifact.txt", "text": "good"},
			{"op": "crew", "args": []string{"submit", "--tests", "pass", "ready"}},
		}})
		task, err := s.Submit(SubmitRequest{Namespace: "task-namespace", Workflow: "quick", Input: string(raw)})
		if err != nil {
			t.Fatal(err)
		}
		return waitTask(t, s, task.Namespace, task.ID, func(task *Task) bool { return terminal(task.Status) })
	}

	first := submit()
	run := first.Runs[0]
	if first.Status != Succeeded || run.Acceptance == nil || run.Acceptance.State != "passed" {
		t.Fatalf("status=%s acceptance=%+v error=%s", first.Status, run.Acceptance, run.Error)
	}
	if len(run.Acceptance.Evidence) != 1 || run.Acceptance.Evidence[0].DurationMS >= 10000 {
		t.Fatalf("evidence duration not bounded: %+v", run.Acceptance.Evidence)
	}
	docker.mu.Lock()
	broken, pending := docker.cleanupFailure, len(docker.pendingCleanup)
	docker.mu.Unlock()
	if broken != nil {
		t.Fatalf("cleanupFailure latched despite a confirmed-stopped check container: %v", broken)
	}
	if pending != 1 {
		t.Fatalf("expected exactly the check container pending cleanup, got %d", pending)
	}

	// The next task must still be admitted, and its admission sweep must
	// remove the container this test left stopped-but-not-yet-removed.
	second := submit()
	run = second.Runs[0]
	if second.Status != Succeeded || run.Acceptance == nil || run.Acceptance.State != "passed" {
		t.Fatalf("next task not admitted/passed: status=%s acceptance=%+v error=%s", second.Status, run.Acceptance, run.Error)
	}
	docker.mu.Lock()
	pending = len(docker.pendingCleanup)
	docker.mu.Unlock()
	if pending != 0 {
		t.Fatalf("admission sweep did not clear the pending check container: %d left", pending)
	}
	ids, err := docker.output(context.Background(), "ps", "-aq", "--filter", "label="+ownerLabel+"="+owner)
	if err != nil || ids != "" {
		t.Fatal("stopped check container was not actually removed by the sweep", err, ids)
	}
}
