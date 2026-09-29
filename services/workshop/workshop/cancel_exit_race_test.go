//go:build linux

package workshop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easygo-agent/rpc"
)

// The fake Docker command and runner return hook place cancellation exactly at
// process exit or after a successful native return; sleeps never trigger it.
func TestCancelExitRace(t *testing.T) {
	for _, overQuota := range []bool{true, false} {
		t.Run(fmt.Sprintf("over_quota=%t", overQuota), func(t *testing.T) {
			r, f, _ := dockerFixture(t)
			// Small internal quota keeps 200 repetitions cheap; public configuration
			// bounds are independently covered by TestDiskQuotaConfigBounds.
			r.cfg.DiskQuotaFiles = 8
			r.cfg.DiskPollMS = 60000
			r.gateway = nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
			var s *Service
			cancelTask := func(in Invocation) error {
				_, err := s.Cancel(in.Namespace, filepath.Base(in.Workspace))
				return err
			}
			runner := artifactRunnerFunc(func(ctx context.Context, in Invocation, emit func(Event) error) (Result, error) {
				f.start = func(_ context.Context, _ *fakeContainer, _ io.Reader, stdout, _ io.Writer) error {
					if err := os.WriteFile(filepath.Join(in.Workspace, "note.md"), []byte("artifact"), 0600); err != nil {
						return err
					}
					fakeSuccess(stdout)
					if overQuota {
						// Cancel before creating excess entries: the live monitor cannot see
						// the excess. Only the final scan can reject this completed process.
						if err := cancelTask(in); err != nil {
							return err
						}
						for i := 0; i < 8; i++ {
							if err := os.WriteFile(filepath.Join(in.Workspace, fmt.Sprint("entry-", i)), nil, 0600); err != nil {
								return err
							}
						}
					}
					return nil
				}
				result, err := r.Run(ctx, in, emit)
				if !overQuota && err == nil {
					// The real Docker/native runner has returned success. Cancel before
					// Service collects/registers its declared artifact.
					err = cancelTask(in)
				}
				return result, err
			})
			var err error
			s, err = New(Config{Root: r.root, ModelGateway: r.gateway, Concurrency: 1, QueueCapacity: 1, RuntimeProfiles: map[string]RuntimeProfile{"fixture": {Engine: "codex", Protocol: "responses", GatewayModel: "model"}}, Workflows: []Workflow{{Name: "fixture", Version: "1", Runtime: "fixture", Instructions: "offline", Policy: "workspace-write", TimeoutSeconds: 10, Artifacts: []string{"note.md"}}}}, runner)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for i := 0; i < 200; i++ {
				task, err := s.Submit(SubmitRequest{Namespace: "owner", Workflow: "fixture", Input: "test"})
				if err != nil {
					t.Fatal(err)
				}
				task = waitTask(t, s, "owner", task.ID, func(task *Task) bool { return terminal(task.Status) })
				run := task.Runs[0]
				want := Cancelled
				if overQuota {
					want = Failed
				}
				if task.Status != want || len(run.Artifacts) != 0 || (overQuota && !strings.HasPrefix(run.Error, "disk_quota_exceeded ")) {
					t.Fatalf("iteration %d: status=%s reason=%q artifacts=%d; want %s with no artifacts", i, task.Status, run.Error, len(run.Artifacts), want)
				}
				if err := os.RemoveAll(task.Workspace); err != nil {
					t.Fatal(err)
				}
			}
			t.Log("200 deterministic cancellation/exit repetitions passed")
		})
	}
}

// A one-nanosecond injected budget expires before scanDiskUsage can traverse
// the root. Cancellation is called synchronously at native process exit.
func TestFinalQuotaBudgetFailure(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancelled=%t", cancelled), func(t *testing.T) {
			r, f, _ := dockerFixture(t)
			r.finalQuotaTimeout = time.Nanosecond
			r.cfg.DiskPollMS = 60000
			r.gateway = nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
			var s *Service
			runner := artifactRunnerFunc(func(ctx context.Context, in Invocation, emit func(Event) error) (Result, error) {
				f.start = func(_ context.Context, _ *fakeContainer, _ io.Reader, stdout, _ io.Writer) error {
					if err := os.WriteFile(filepath.Join(in.Workspace, "note.md"), []byte("artifact"), 0600); err != nil {
						return err
					}
					fakeSuccess(stdout)
					if cancelled {
						_, err := s.Cancel(in.Namespace, filepath.Base(in.Workspace))
						return err
					}
					return nil
				}
				return r.Run(ctx, in, emit)
			})
			var err error
			s, err = New(Config{Root: r.root, ModelGateway: r.gateway, Concurrency: 1, QueueCapacity: 1, RuntimeProfiles: map[string]RuntimeProfile{"fixture": {Engine: "codex", Protocol: "responses", GatewayModel: "model"}}, Workflows: []Workflow{{Name: "fixture", Version: "1", Runtime: "fixture", Instructions: "offline", Policy: "workspace-write", TimeoutSeconds: 10, Artifacts: []string{"note.md"}}}}, runner)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for i := 0; i < 200; i++ {
				task, err := s.Submit(SubmitRequest{Namespace: "owner", Workflow: "fixture", Input: "test"})
				if err != nil {
					t.Fatal(err)
				}
				task = waitTask(t, s, "owner", task.ID, func(task *Task) bool { return terminal(task.Status) })
				run := task.Runs[0]
				if task.Status != Failed || !strings.HasPrefix(run.Error, "disk_quota_scan_failed:") || !strings.Contains(run.Error, "context deadline exceeded") || len(run.Artifacts) != 0 {
					t.Fatalf("iteration %d: status=%s reason=%q artifacts=%d", i, task.Status, run.Error, len(run.Artifacts))
				}
				if err := os.RemoveAll(task.Workspace); err != nil {
					t.Fatal(err)
				}
			}
			t.Log("200 deterministic final-scan timeout repetitions passed")
		})
	}
}
