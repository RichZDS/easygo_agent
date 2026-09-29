package workshop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
