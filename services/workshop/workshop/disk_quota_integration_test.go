//go:build linux

package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/rpc"
	"github.com/google/uuid"
)

// All cases use 16 MiB quotas and at most 128 MiB target bytes. Each case closes its
// controller and immediately removes its own workspace before the next begins.
func TestDockerDiskQuotaIntegration(t *testing.T) {
	endpoint, image := os.Getenv("EASYGO_DOCKER_TEST_ENDPOINT"), os.Getenv("EASYGO_DOCKER_QUOTA_IMAGE")
	if endpoint == "" || image == "" {
		t.Skip("explicit dedicated endpoint and quota fixture image required")
	}
	for _, tc := range []struct {
		name                      string
		bytes                     int64
		files, chunk, delay, poll int
		sparse                    bool
		wantQuota                 bool
	}{
		{"hard_file", 32 << 20, 1, 1 << 20, 0, 200, false, true},
		{"aggregate_bytes", 1 << 20, 64, 256 << 10, 5, 200, false, true},
		{"aggregate_files", 1, 1600, 1, 1, 200, false, true},
		{"final_scan", 1 << 20, 24, 1 << 20, 0, 60000, false, true},
		{"sparse", 8 << 20, 2, 4096, 0, 200, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, e := os.MkdirTemp("/tmp", "quota-proof-")
			if e != nil {
				t.Fatal(e)
			}
			defer os.RemoveAll(root)
			gateway := nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) {
				return map[string]any{"content_type": "application/json", "body": []byte(`{"fixture":"ok"}`)}, nil
			})
			cfg := Config{Root: root, Concurrency: 2, QueueCapacity: 2, ModelGateway: gateway, Engines: map[string]EngineConfig{"codex": {}}, RuntimeProfiles: map[string]RuntimeProfile{"fixture": {Engine: "codex", Protocol: "responses", GatewayModel: "fixture-model"}}, Workflows: []Workflow{{Name: "quota", Version: "1", Runtime: "fixture", Instructions: "offline bounded fixture", Policy: "workspace-write", TimeoutSeconds: 15, Artifacts: []string{"artifact.txt"}}}, Sandbox: SandboxConfig{Mode: "docker", DockerBinary: os.Getenv("EASYGO_DOCKER_TEST_BINARY"), Endpoint: endpoint, Image: image, Owner: "quota-" + uuid.NewString(), HostRoot: root, DiskQuotaBytes: 16 << 20, DiskQuotaFiles: 1000, DiskPollMS: tc.poll}}
			s, e := New(cfg, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			r := s.runner.(*DockerRunner)
			var creates atomic.Int64
			command := r.command
			r.command = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
				if args[0] == "create" {
					creates.Add(1)
				}
				return command(ctx, stdin, stdout, stderr, args...)
			}
			input, _ := json.Marshal(map[string]any{"mode": "fill", "bytes": tc.bytes, "files": tc.files, "chunk": tc.chunk, "delay_ms": tc.delay, "sparse": tc.sparse})
			start := time.Now()
			task, e := s.Submit(SubmitRequest{Namespace: "task-namespace", Workflow: "quota", Input: string(input)})
			if e != nil {
				t.Fatal(e)
			}
			var normal *Task
			if tc.name == "aggregate_bytes" {
				normal, e = s.Submit(SubmitRequest{Namespace: "task-namespace", Workflow: "quota", Input: `{"mode":"first"}`})
				if e != nil {
					t.Fatal(e)
				}
			}
			task = waitTask(t, s, "task-namespace", task.ID, func(task *Task) bool { return terminal(task.Status) })
			elapsed := time.Since(start)
			run := task.Runs[len(task.Runs)-1]
			if tc.wantQuota {
				if task.Status != Failed || !strings.HasPrefix(run.Error, "disk_quota_exceeded ") || len(run.Artifacts) != 0 {
					t.Fatalf("quota outcome status=%s error=%s artifacts=%v", task.Status, run.Error, run.Artifacts)
				}
			} else if task.Status != Succeeded || len(run.Artifacts) != 1 {
				t.Fatalf("sparse outcome=%s %s", task.Status, run.Error)
			}
			usage, e := scanDiskUsage(context.Background(), task.Workspace, 256<<20, 10000)
			if e != nil {
				t.Fatal(e)
			}
			var meta struct {
				Started int64  `json:"started_unix_nano"`
				Soft    uint64 `json:"fsize_soft"`
				Hard    uint64 `json:"fsize_hard"`
			}
			raw, e := os.ReadFile(filepath.Join(task.Workspace, "fill-meta.json"))
			if e != nil || json.Unmarshal(raw, &meta) != nil {
				t.Fatal("missing rlimit proof", e)
			}
			if meta.Soft != 16<<20 || meta.Hard != 16<<20 {
				t.Fatal("fsize units/limits wrong", meta)
			}
			latency := "unmarked"
			if raw, e := os.ReadFile(filepath.Join(task.Workspace, "fill-crossed.json")); e == nil {
				var marker struct {
					At int64 `json:"unix_nano"`
				}
				if json.Unmarshal(raw, &marker) == nil {
					delay := time.Since(time.Unix(0, marker.At))
					latency = delay.String()
					if tc.poll == 200 && delay > 1500*time.Millisecond {
						t.Fatalf("poll cleanup too slow: %v", delay)
					}
				}
			}
			t.Logf("case=%s poll_ms=%d elapsed=%s stopped_usage=%+v byte_overshoot=%d file_overshoot=%d payload_cross_to_terminal=%s error=%s", tc.name, tc.poll, elapsed, usage, max(0, usage.UsedBytes-(16<<20)), max(0, usage.UsedFiles-1000), latency, run.Error)
			if tc.name == "hard_file" {
				file, e := os.Stat(filepath.Join(task.Workspace, "fill", "file-000000"))
				if e != nil || file.Size() > 16<<20 {
					t.Fatal("hard file bound failed", e)
				}
				events, e := s.Events("task-namespace", task.ID, 0)
				if e != nil {
					t.Fatal(e)
				}
				found := false
				for _, event := range events {
					if event.Kind == "diagnostic" && strings.Contains(event.Text, "file too large") {
						found = true
					}
				}
				if !found {
					t.Fatal("missing EFBIG/SIGXFSZ evidence")
				}
				t.Logf("EFBIG observed; final file size=%d", file.Size())
			}
			if normal != nil {
				normal = waitTask(t, s, "task-namespace", normal.ID, func(task *Task) bool { return terminal(task.Status) })
				if normal.Status != Succeeded {
					t.Fatal("concurrent normal task harmed", normal.Status, normal.Runs[0].Error)
				}
				t.Log("concurrent normal task succeeded")
			}
			if tc.wantQuota {
				before := creates.Load()
				if _, e = s.Resume("task-namespace", task.ID, string(input)); !errors.Is(e, ErrDiskQuotaExceeded) {
					t.Fatal("overquota resume accepted", e)
				}
				if creates.Load() != before {
					t.Fatal("rejected resume created container")
				}
				persisted, e := s.Get("task-namespace", task.ID)
				if e != nil || len(persisted.Runs) != 1 || persisted.Status != Failed {
					t.Fatal("resume rejection changed task")
				}
			}

			ids, e := r.output(context.Background(), "ps", "-aq", "--filter", "label="+ownerLabel+"="+r.cfg.Owner)
			if e != nil || ids != "" {
				t.Fatal("quota left owned container", ids, e)
			}
		})
	}
}
