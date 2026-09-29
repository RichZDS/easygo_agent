//go:build linux

package workshop

import (
	"context"
	"easygo-agent/rpc"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDiskQuotaConfigBounds(t *testing.T) {
	base := SandboxConfig{Mode: "docker", Image: "fixture", Owner: "test", HostRoot: "/tmp/data", Endpoint: "unix:///tmp/docker.sock"}
	normalized, e := normalizeSandbox(base)
	if e != nil {
		t.Fatal(e)
	}
	if normalized.DiskQuotaBytes != 2<<30 || normalized.DiskQuotaFiles != 200000 || normalized.DiskPollMS != 2000 {
		t.Fatal("quota defaults", normalized)
	}
	for _, edit := range []func(*SandboxConfig){func(c *SandboxConfig) { c.DiskQuotaBytes = 16<<20 - 1 }, func(c *SandboxConfig) { c.DiskQuotaBytes = 1<<40 + 1 }, func(c *SandboxConfig) { c.DiskQuotaFiles = 999 }, func(c *SandboxConfig) { c.DiskQuotaFiles = 10000001 }, func(c *SandboxConfig) { c.DiskPollMS = 199 }, func(c *SandboxConfig) { c.DiskPollMS = 60001 }} {
		cfg := base
		edit(&cfg)
		if _, e := normalizeSandbox(cfg); e == nil {
			t.Fatal("out-of-range quota accepted", cfg)
		}
	}
	for _, cfg := range []SandboxConfig{
		{Mode: "docker", Image: "fixture", Owner: "test", HostRoot: "/tmp/data", Endpoint: "unix:///tmp/docker.sock", DiskQuotaBytes: 16 << 20, DiskQuotaFiles: 1000, DiskPollMS: 200},
		{Mode: "docker", Image: "fixture", Owner: "test", HostRoot: "/tmp/data", Endpoint: "unix:///tmp/docker.sock", DiskQuotaBytes: 1 << 40, DiskQuotaFiles: 10000000, DiskPollMS: 60000},
	} {
		if _, e := normalizeSandbox(cfg); e != nil {
			t.Fatal(e)
		}
	}
	r, _, _ := dockerFixture(t)
	if option(r.containerOptions("a", "/tmp/workspace", "", false), "--ulimit") != "fsize=2147483648:2147483648" {
		t.Fatal("fsize must use byte-valued soft/hard limits")
	}
}

func allocated(t *testing.T, path string) int64 {
	t.Helper()
	var st unix.Stat_t
	if e := unix.Lstat(path, &st); e != nil {
		t.Fatal(e)
	}
	return st.Blocks * 512
}

func TestDiskUsageNoFollowHardlinksSparseAndEntries(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if e := os.WriteFile(filepath.Join(outside, "outside"), make([]byte, 1<<20), 0600); e != nil {
		t.Fatal(e)
	}
	home := filepath.Join(root, ".workshop-home")
	os.Mkdir(home, 0700)
	original := filepath.Join(home, "state")
	os.WriteFile(original, make([]byte, 8192), 0600)
	linked := filepath.Join(root, "alias")
	os.Link(original, linked)
	symlink := filepath.Join(root, "outside-link")
	os.Symlink(outside, symlink)
	sparse := filepath.Join(root, "sparse")
	f, e := os.Create(sparse)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Seek(64<<20, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write([]byte{1}); e != nil {
		t.Fatal(e)
	}
	f.Close()
	usage, e := scanDiskUsage(context.Background(), root, 16<<20, 1000)
	if e != nil {
		t.Fatal(e)
	}
	want := allocated(t, root) + allocated(t, home) + allocated(t, original) + allocated(t, symlink) + allocated(t, sparse)
	if usage.UsedBytes != want || usage.UsedFiles != 6 {
		t.Fatalf("usage %+v want bytes=%d files=6", usage, want)
	}
	info, _ := os.Stat(sparse)
	if info.Size() <= 64<<20 || usage.UsedBytes >= 1<<20 {
		t.Fatal("sparse file charged by logical size or link followed")
	}
	_, e = scanDiskUsage(context.Background(), root, 16<<20, 2)
	var quota *DiskQuotaError
	if !errors.As(e, &quota) || quota.UsedFiles != 3 {
		t.Fatalf("entry cap not immediate: %v", e)
	}
	_, e = scanDiskUsage(context.Background(), root, 1, 1000)
	if !errors.Is(e, ErrDiskQuotaExceeded) {
		t.Fatal("byte cap ignored", e)
	}
}

func TestDiskUsageFailsClosedAndCancels(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	os.Mkdir(locked, 0000)
	defer os.Chmod(locked, 0700)
	if os.Geteuid() != 0 {
		if _, e := scanDiskUsage(context.Background(), root, 16<<20, 1000); !errors.Is(e, ErrDiskQuotaScanFailed) {
			t.Fatal("permission failure ignored", e)
		}
	}
	if _, e := scanDiskUsage(context.Background(), filepath.Join(root, "missing"), 16<<20, 1000); !errors.Is(e, ErrDiskQuotaScanFailed) {
		t.Fatal("IO failure ignored", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := scanDiskUsage(ctx, t.TempDir(), 16<<20, 1000); !errors.Is(e, context.Canceled) {
		t.Fatal("cancel ignored", e)
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(root, link)
	if _, e := scanDiskUsage(context.Background(), link, 16<<20, 1000); !errors.Is(e, ErrDiskQuotaScanFailed) {
		t.Fatal("root symlink accepted", e)
	}
}

func TestDiskQuotaResumeRejectsBeforeQueue(t *testing.T) {
	s, task := completedArtifact(t, []byte("ok"))
	r, _, _ := dockerFixture(t)
	r.root = s.root
	r.cfg.HostRoot = s.root
	r.cfg.DiskQuotaFiles = 1
	s.runner = r
	before := len(task.Runs)
	if _, e := s.Resume("owner", task.ID, "again"); !errors.Is(e, ErrDiskQuotaExceeded) {
		t.Fatal("overquota resume accepted", e)
	}
	got, e := s.Get("owner", task.ID)
	if e != nil || len(got.Runs) != before || got.Status != Succeeded {
		t.Fatal("rejected resume mutated state")
	}
	// Host mode remains unchanged, even for the same workspace.
	s.runner = artifactRunnerFunc(func(ctx context.Context, in Invocation, emit func(Event) error) (Result, error) {
		return writeDownloadFixture(in, []byte("again"))
	})
	if _, e = s.Resume("owner", task.ID, "again"); e != nil {
		t.Fatal(e)
	}
	waitTask(t, s, "owner", task.ID, func(t *Task) bool { return t.Status == Succeeded })
}

func TestDiskUsageHugeDirectoryStopsAtEntryLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 1100; i++ {
		f, e := os.CreateTemp(root, "entry-")
		if e != nil {
			t.Fatal(e)
		}
		f.Close()
	}
	start := time.Now()
	usage, e := scanDiskUsage(context.Background(), root, 16<<20, 1000)
	if !errors.Is(e, ErrDiskQuotaExceeded) || usage.UsedFiles != 1001 {
		t.Fatal("unbounded enumeration", usage, e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("unexpectedly slow bounded scan")
	}
}

func TestDiskQuotaLiveScanFailureStopsContainerAndFailsTask(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-denial proof requires nonroot controller UID")
	}
	r, f, _ := dockerFixture(t)
	r.cfg.DiskPollMS = 200
	r.gateway = nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
	var locked string
	f.start = func(ctx context.Context, c *fakeContainer, stdin io.Reader, stdout, stderr io.Writer) error {
		// Find the operator-controlled workspace source, then deny directory reads.
		for i, arg := range c.args {
			if arg == "--mount" && strings.Contains(c.args[i+1], ",dst=/workspace,") {
				source := strings.Split(strings.TrimPrefix(c.args[i+1], "type=bind,src="), ",")[0]
				locked = filepath.Join(source, ".workshop-home", "unreadable")
			}
		}
		if e := os.Mkdir(locked, 0000); e != nil {
			return e
		}
		fakeSuccess(stdout) // Even a native terminal success must not publish artifacts.
		<-ctx.Done()
		return ctx.Err()
	}
	s, e := New(Config{Root: r.root, Concurrency: 1, ModelGateway: r.gateway, RuntimeProfiles: map[string]RuntimeProfile{"fixture": {Engine: "codex", Protocol: "responses", GatewayModel: "model"}}, Workflows: []Workflow{{Name: "fixture", Version: "1", Runtime: "fixture", Instructions: "offline", Policy: "workspace-write", TimeoutSeconds: 5}}}, r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	task, e := s.Submit(SubmitRequest{Namespace: "task-namespace", Workflow: "fixture", Input: "test"})
	if e != nil {
		t.Fatal(e)
	}
	task = waitTask(t, s, "task-namespace", task.ID, func(task *Task) bool { return terminal(task.Status) })
	if locked != "" {
		defer os.Chmod(locked, 0700)
	}
	run := task.Runs[0]
	if task.Status != Failed || run.Error != "disk_quota_scan_failed" || len(run.Artifacts) != 0 || len(f.containers) != 0 {
		t.Fatal("scan failure not fail-closed", task.Status, run.Error)
	}
}
