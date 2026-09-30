package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"easygo-agent/rpc"
	"github.com/google/uuid"
)

type fakeContainer struct {
	Name   string
	Config struct{ Labels map[string]string }
	ID     string
	args   []string
	State  struct {
		Status     string
		Running    bool
		Paused     bool
		Restarting bool
		ExitCode   int
		Error      string
	}
	// Test-only cleanup-resilience knobs, invisible to JSON encoding (lowercase).
	rmFailReal      int  // rm fails this many times without deleting, then proceeds normally
	rmAmbiguousOnce bool // next rm deletes the container but still reports an error
	psFails         bool // ps targeting this name fails outright (transport error)
	inspectFails    bool // inspect targeting this name/ID fails outright
}
type fakeDocker struct {
	t                      *testing.T
	containers             map[string]*fakeContainer
	calls                  [][]string
	start                  func(context.Context, *fakeContainer, io.Reader, io.Writer, io.Writer) error
	failCreate, failRemove bool
}

func (f *fakeDocker) command(ctx context.Context, in io.Reader, out, diag io.Writer, args ...string) error {
	f.calls = append(f.calls, slices.Clone(args))
	switch args[0] {
	case "image":
		io.WriteString(out, "sha256:"+strings.Repeat("a", 64))
		return nil
	case "create":
		c := &fakeContainer{ID: uuid.NewString(), args: slices.Clone(args)}
		c.Config.Labels = map[string]string{}
		for i := 1; i < len(args)-1; i++ {
			switch args[i] {
			case "--name":
				c.Name = args[i+1]
			case "--label":
				k, v, _ := strings.Cut(args[i+1], "=")
				c.Config.Labels[k] = v
			}
		}
		f.containers[c.Name] = c
		if f.failCreate {
			return errors.New("ambiguous create")
		}
		io.WriteString(out, c.ID)
		return nil
	case "ps":
		for i := 2; i < len(args)-1; i++ {
			if args[i] == "--filter" && strings.HasPrefix(args[i+1], "name=") {
				name := strings.TrimSuffix(strings.TrimPrefix(args[i+1], "name=^/"), "$")
				if c, ok := f.containers[name]; ok && c.psFails {
					return errors.New("ps failed")
				}
			}
		}
		for name, c := range f.containers {
			match := true
			for i := 2; i < len(args)-1; i++ {
				if args[i] != "--filter" {
					continue
				}
				filter := args[i+1]
				if strings.HasPrefix(filter, "name=") {
					match = match && filter == "name=^/"+name+"$"
				} else {
					k, v, _ := strings.Cut(strings.TrimPrefix(filter, "label="), "=")
					match = match && c.Config.Labels[k] == v
				}
			}
			if match {
				io.WriteString(out, c.ID+"\n")
			}
		}
		return nil
	case "inspect":
		for name, c := range f.containers {
			if args[1] == name || args[1] == c.ID {
				if c.inspectFails {
					return errors.New("inspect failed")
				}
				return json.NewEncoder(out).Encode([]*fakeContainer{c})
			}
		}
		return errors.New("missing")
	case "rm":
		if f.failRemove {
			return errors.New("daemon gone")
		}
		for name, c := range f.containers {
			if c.ID == args[2] {
				if c.rmFailReal > 0 {
					c.rmFailReal--
					return errors.New("rm failed")
				}
				if c.rmAmbiguousOnce {
					c.rmAmbiguousOnce = false
					delete(f.containers, name)
					return errors.New("rm ambiguous: daemon may have already removed it")
				}
				delete(f.containers, name)
				return nil
			}
		}
		return errors.New("missing")
	case "start":
		c := f.containers[args[len(args)-1]]
		if c == nil {
			return errors.New("missing")
		}
		if i := slices.Index(c.args, "--verify-root"); i >= 0 {
			io.WriteString(out, c.args[i+1])
			return nil
		}
		return f.start(ctx, c, in, out, diag)
	}
	f.t.Fatalf("unexpected Docker operation: %v", args)
	return nil
}
func dockerFixture(t *testing.T) (*DockerRunner, *fakeDocker, Invocation) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "docker-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	c, e := normalizeSandbox(SandboxConfig{Mode: "docker", Image: "fixture:local", Owner: "test-owner", Endpoint: "unix:///tmp/dummy-docker.sock", HostRoot: root})
	if e != nil {
		t.Fatal(e)
	}
	f := &fakeDocker{t: t, containers: map[string]*fakeContainer{}}
	r := &DockerRunner{cfg: c, root: root, maxOutput: defaultOutputLimit, command: f.command}
	if e = r.Initialize(context.Background()); e != nil {
		t.Fatal(e)
	}
	workspace := filepath.Join(root, "workspaces", uuid.NewString())
	if e = os.MkdirAll(workspace, 0700); e != nil {
		t.Fatal(e)
	}
	p := RuntimeProfile{Engine: "codex", Protocol: "responses", GatewayModel: "selected"}
	in := Invocation{Namespace: "task-namespace", Workspace: workspace, Workflow: Workflow{Name: "test", Version: "1", Engine: "codex", Model: "selected", Instructions: "fixture", Policy: "workspace-write", TimeoutSeconds: 10, RuntimeSpec: &p}}
	return r, f, in
}
func fakeSuccess(out io.Writer) {
	io.WriteString(out, `{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}`+"\n"+`{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":3}}`+"\n")
}
func option(args []string, k string) string {
	i := slices.Index(args, k)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

// mountFields splits a Docker --mount value into its comma-separated fields,
// e.g. "type=bind,src=/a,dst=/b,readonly" -> ["type=bind","src=/a","dst=/b","readonly"].
func mountFields(spec string) []string {
	return strings.Split(spec, ",")
}

// findMount returns the field set of the --mount value whose fields include
// dst=<dst>, and whether one was found. Matching on the exact dst= field
// (not a substring of the whole value) and returning a field set instead of
// a string means callers never depend on where in the mount string that
// field, or "readonly", happens to sit.
func findMount(args []string, dst string) ([]string, bool) {
	target := "dst=" + dst
	for i, arg := range args {
		if arg != "--mount" || i+1 >= len(args) {
			continue
		}
		fields := mountFields(args[i+1])
		if slices.Contains(fields, target) {
			return fields, true
		}
	}
	return nil, false
}

func TestDockerOptionsAndPathMapping(t *testing.T) {
	r, _, in := dockerFixture(t)
	r.cfg.HostRoot = "/daemon/workshop"
	mapped, e := r.hostPath(in.Workspace)
	if e != nil || mapped != "/daemon/workshop/workspaces/"+filepath.Base(in.Workspace) {
		t.Fatalf("mapping %s %v", mapped, e)
	}
	args := r.containerOptions("run", mapped, "/daemon/workshop/relays/run", false)
	for k, v := range map[string]string{"--user": "1000:1000", "--network": "none", "--cap-drop": "ALL", "--security-opt": "no-new-privileges=true", "--pids-limit": "128", "--memory": "1073741824", "--memory-swap": "1073741824", "--cpus": "1.000000000", "--pull": "never", "--log-driver": "none"} {
		if option(args, k) != v {
			t.Errorf("%s=%s", k, option(args, k))
		}
	}
	if !slices.Contains(args, "--read-only") || strings.Contains(strings.Join(args, " "), "docker.sock") {
		t.Fatal(args)
	}
	if _, e = r.hostPath(r.root); e == nil {
		t.Fatal("root exposed")
	}
	if _, e = r.hostPath(t.TempDir()); e == nil {
		t.Fatal("outside exposed")
	}
	link := filepath.Join(r.root, "link")
	if e = os.Symlink(in.Workspace, link); e != nil {
		t.Fatal(e)
	}
	if _, e = r.hostPath(link); e == nil {
		t.Fatal("symlink accepted")
	}
	for _, c := range []SandboxConfig{{Mode: "docker"}, {Mode: "docker", Image: "x", Owner: "ok", HostRoot: "/ok", Endpoint: "tcp://localhost:2375"}, {Mode: "docker", Image: "x", Owner: "ok", HostRoot: "/a/../b", Endpoint: "unix:///tmp/docker.sock"}} {
		if _, e = normalizeSandbox(c); e == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestDockerRunUsesUDSOnlyAndCleansUp(t *testing.T) {
	r, f, in := dockerFixture(t)
	r.gateway = nativeRelayFixture(t, func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
		return map[string]any{"content_type": "application/json", "body": []byte(`{"ok":true}`)}, nil
	})
	var names []string
	f.start = func(ctx context.Context, c *fakeContainer, stdin io.Reader, out, diag io.Writer) error {
		names = append(names, c.Name)
		env := map[string]string{}
		socketDir := ""
		for i, a := range c.args {
			if a == "--env" {
				k, v, _ := strings.Cut(c.args[i+1], "=")
				env[k] = v
			}
			if a == "--mount" && strings.Contains(c.args[i+1], "/run/easygo-relay") {
				socketDir = strings.Split(strings.TrimPrefix(c.args[i+1], "type=bind,src="), ",")[0]
			}
		}
		if env["EASYGO_CREW_URL"] != "http://127.0.0.1:18080/crew" || env["EASYGO_CREW_TOKEN"] != env["EASYGO_RUNTIME_API_KEY"] {
			t.Fatal("crew environment missing or not run-scoped")
		}
		if env["HOME"] != "/workspace/.workshop-home" || env["EASYGO_RELAY_SOCKET"] != "/run/easygo-relay/model.sock" || env["EASYGO_RUNTIME_API_KEY"] == "" {
			t.Fatal(env)
		}
		if strings.Contains(strings.Join(c.args, " "), r.gateway.TLS.KeyFile) {
			t.Fatal("TLS key mounted")
		}
		tr := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(socketDir, "model.sock"))
		}}
		defer tr.CloseIdleConnections()
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://relay/v1/responses", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+env["EASYGO_RUNTIME_API_KEY"])
		resp, e := (&http.Client{Transport: tr}).Do(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("relay status %d", resp.StatusCode)
		}
		io.WriteString(diag, env["EASYGO_RUNTIME_API_KEY"])
		fakeSuccess(out)
		return nil
	}
	for i := 0; i < 2; i++ {
		res, e := r.Run(context.Background(), in, func(event Event) error {
			if event.Kind == "diagnostic" && event.Text != "[REDACTED]" {
				t.Fatal("capability leaked")
			}
			return nil
		})
		if e != nil || res.SessionID == "" {
			t.Fatalf("%+v %v", res, e)
		}
		in.SessionID = res.SessionID
	}
	if names[0] == names[1] || len(f.containers) != 0 {
		t.Fatal("container reuse/leak")
	}
	dirs, e := os.ReadDir(filepath.Join(r.root, "relays"))
	if e != nil || len(dirs) != 0 {
		t.Fatal("relay leak", e)
	}
}

func TestDockerCancelAmbiguousCreateAndCleanupFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "ambiguous-create", "cleanup-failure", "parser-failure"} {
		t.Run(mode, func(t *testing.T) {
			r, f, in := dockerFixture(t)
			r.gateway = nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.failCreate = mode == "ambiguous-create"
			f.start = func(ctx context.Context, c *fakeContainer, stdin io.Reader, out, diag io.Writer) error {
				switch mode {
				case "cancel":
					cancel()
					<-ctx.Done()
					return ctx.Err()
				case "cleanup-failure":
					f.failRemove = true
					fakeSuccess(out)
				case "parser-failure":
					io.WriteString(out, "not-json\n")
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}
			_, e := r.Run(ctx, in, func(Event) error { return nil })
			if e == nil {
				t.Fatal("failure accepted")
			}
			if mode == "cleanup-failure" {
				before := len(f.calls)
				if _, e = r.Run(context.Background(), in, func(Event) error { return nil }); e == nil || len(f.calls) != before {
					t.Fatal("admission continued after cleanup failed")
				}
			} else if len(f.containers) != 0 {
				t.Fatal("container leaked")
			}
		})
	}
}

func TestDockerReapOnlyOwnedAndRequiresMapping(t *testing.T) {
	r, f, _ := dockerFixture(t)
	for _, name := range []string{"owned", "unrelated", "other-root"} {
		c := &fakeContainer{Name: name, ID: uuid.NewString()}
		c.Config.Labels = r.labels()
		if name == "unrelated" {
			c.Config.Labels[ownerLabel] = "another"
		}
		if name == "other-root" {
			c.Config.Labels[rootLabel] = "/another/root"
		}
		f.containers[name] = c
	}
	if e := r.Initialize(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(f.containers) != 2 || f.containers["owned"] != nil {
		t.Fatal("incorrect orphan cleanup")
	}
	if e := r.remove(context.Background(), "unrelated"); e == nil {
		t.Fatal("removed foreign container")
	}
	r.initialized = false
	if _, e := r.Run(context.Background(), Invocation{}, func(Event) error { return nil }); e == nil {
		t.Fatal("unattested runner accepted")
	}
}

func TestDockerRejectsDirectCredentialsAndReplacementRunner(t *testing.T) {
	r, _, in := dockerFixture(t)
	in.Workflow.RuntimeSpec = &RuntimeProfile{Engine: "codex", Protocol: "responses", Model: "dummy", BaseURL: "https://dummy.invalid", APIKeyEnv: "DUMMY_KEY"}
	if _, e := r.Run(context.Background(), in, func(Event) error { return nil }); e == nil {
		t.Fatal("direct credential accepted")
	}
	cfg := Config{Root: t.TempDir(), Concurrency: 1, Sandbox: SandboxConfig{Mode: "docker"}}
	if _, e := New(cfg, r); e == nil {
		t.Fatal("replacement runner bypass")
	}
	cfg.Sandbox.Mode = "typo"
	if _, e := New(cfg, nil); e == nil {
		t.Fatal("mode typo fell back")
	}
}

func TestDockerRuntimeConfigPathsAllEngines(t *testing.T) {
	for _, engine := range []string{"codex", "claude", "pi", "openclaw"} {
		t.Run(engine, func(t *testing.T) {
			r, f, in := dockerFixture(t)
			in.Workflow.Engine = engine
			in.Workflow.RuntimeSpec.Engine = engine
			if engine == "claude" {
				in.Workflow.RuntimeSpec.Protocol = "anthropic"
			}
			r.gateway = nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
			f.start = func(ctx context.Context, c *fakeContainer, stdin io.Reader, out, diag io.Writer) error {
				// Config files must be written locally but contain only in-container paths.
				for _, path := range []string{"pi/models.json", "openclaw/openclaw.json"} {
					raw, e := os.ReadFile(filepath.Join(in.Workspace, ".workshop-home", path))
					if e == nil && strings.Contains(string(raw), r.root) {
						t.Fatal("controller path leaked into config")
					}
				}
				if engine == "pi" || engine == "openclaw" {
					if _, e := os.Stat(filepath.Join(in.Workspace, ".workshop-home", map[string]string{"pi": "pi/models.json", "openclaw": "openclaw/openclaw.json"}[engine])); e != nil {
						t.Fatal(e)
					}
				}
				return errors.New("stop before native parser; configuration checked")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r.Run(ctx, in, func(Event) error { return nil })
			if len(f.containers) != 0 {
				t.Fatal("leak")
			}
		})
	}
}

func TestDockerRejectsHomeSymlinkWithoutOutsideWrites(t *testing.T) {
	r, _, in := dockerFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(in.Workspace, ".workshop-home")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), in, func(Event) error { return nil }); err == nil {
		t.Fatal("symlink HOME accepted")
	}
	if err := mkdirNoSymlinks(filepath.Join(in.Workspace, ".workshop-home", "must-not-exist"), 0700); err == nil {
		t.Fatal("symlink mkdir accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("outside directory mutated", err)
	}
}

func TestDockerReadOnlyWorkspaceAllowsOnlyNativeHomeWrites(t *testing.T) {
	r, _, _ := dockerFixture(t)
	args := r.taskContainerOptions("fixture", "/host/workspace", "/host/relay", "/host/workspace/.workshop-home", "read-only")
	mounts := []string{}
	for i, arg := range args {
		if arg == "--mount" {
			mounts = append(mounts, args[i+1])
		}
	}
	if len(mounts) != 3 || !strings.HasSuffix(mounts[0], ",readonly") || !strings.Contains(mounts[2], "dst=/workspace/.workshop-home,") || strings.Contains(mounts[2], ",readonly") {
		t.Fatalf("read-only mounts: %v", mounts)
	}
}

// TestContainerOptionsWorkspaceReadOnlyByFieldSet asserts, by parsing the
// --mount field set rather than locking a substring of the whole value, that
// workspace read-only-ness is decided once at construction and holds for the
// check container, both task policies, and a resumed run.
func TestContainerOptionsWorkspaceReadOnlyByFieldSet(t *testing.T) {
	r, f, in := dockerFixture(t)

	checkArgs := r.checkContainerOptions("check", "/host/work", "/host/checks", AcceptanceCheck{Command: []string{"/pack/checks/unit"}})
	if mount, ok := findMount(checkArgs, runtimeWorkspace); !ok || !slices.Contains(mount, "readonly") {
		t.Fatalf("check container workspace not readonly: %v", mount)
	}

	roArgs := r.taskContainerOptions("task-ro", "/host/workspace", "/host/relay", "/host/workspace/.workshop-home", "read-only")
	if mount, ok := findMount(roArgs, runtimeWorkspace); !ok || !slices.Contains(mount, "readonly") {
		t.Fatalf("read-only task workspace not readonly: %v", mount)
	}
	if mount, ok := findMount(roArgs, runtimeWorkspace+"/.workshop-home"); !ok || slices.Contains(mount, "readonly") {
		t.Fatalf(".workshop-home submount should stay writable: %v", mount)
	}

	wArgs := r.taskContainerOptions("task-write", "/host/workspace", "/host/relay", "/host/workspace/.workshop-home", "workspace-write")
	if mount, ok := findMount(wArgs, runtimeWorkspace); !ok || slices.Contains(mount, "readonly") {
		t.Fatalf("workspace-write task workspace should stay writable: %v", mount)
	}

	// Covers resume: the same read-only decision must hold on a second Run
	// that carries a session forward, not just on a freshly submitted task.
	in.Workflow.Policy = "read-only"
	r.gateway = nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
	seen := 0
	f.start = func(ctx context.Context, c *fakeContainer, stdin io.Reader, out, diag io.Writer) error {
		if mount, ok := findMount(c.args, runtimeWorkspace); !ok || !slices.Contains(mount, "readonly") {
			t.Fatalf("run %d: workspace not readonly: %v", seen, mount)
		}
		seen++
		fakeSuccess(out)
		return nil
	}
	for i := 0; i < 2; i++ {
		res, e := r.Run(context.Background(), in, func(Event) error { return nil })
		if e != nil {
			t.Fatal(e)
		}
		in.SessionID = res.SessionID
	}
	if seen != 2 {
		t.Fatalf("expected 2 runs checked (initial + resume), got %d", seen)
	}
}

func TestCodexInnerPolicyChangesOnlyAfterDockerInitialization(t *testing.T) {
	for _, policy := range []string{"read-only", "workspace-write"} {
		t.Run(policy, func(t *testing.T) {
			r, f, in := dockerFixture(t)
			in.Workflow.Policy = Policy(policy)
			hostArgs, err := engineArgs(in)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(hostArgs, `sandbox_mode="`+policy+`"`) || slices.Contains(hostArgs, `sandbox_mode="danger-full-access"`) {
				t.Fatal("host policy changed")
			}
			r.gateway = nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
			f.start = func(ctx context.Context, c *fakeContainer, stdin io.Reader, out, diag io.Writer) error {
				if !slices.Contains(c.args, `sandbox_mode="danger-full-access"`) || !slices.Contains(c.args, `approval_policy="never"`) {
					t.Fatal("missing Docker-only native overrides")
				}
				if !slices.Contains(c.args, "--read-only") || option(c.args, "--network") != "none" || option(c.args, "--user") != "1000:1000" || option(c.args, "--cap-drop") != "ALL" || option(c.args, "--security-opt") != "no-new-privileges=true" {
					t.Fatal("outer isolation changed")
				}
				workspaceRO := false
				for i, arg := range c.args {
					if arg == "--mount" && strings.Contains(c.args[i+1], ",dst=/workspace,") {
						workspaceRO = strings.HasSuffix(c.args[i+1], ",readonly")
					}
				}
				if workspaceRO != (policy == "read-only") {
					t.Fatal("workspace policy not enforced")
				}
				fakeSuccess(out)
				return nil
			}
			if _, err = r.Run(context.Background(), in, func(Event) error { return nil }); err != nil {
				t.Fatal(err)
			}
			r.initialized = false
			before := len(f.calls)
			if _, err = r.Run(context.Background(), in, func(Event) error { return nil }); err == nil || len(f.calls) != before {
				t.Fatal("override ran without initialized Docker")
			}
		})
	}
}

// fastCleanupBackoff shortens cleanup's retry backoff for tests without
// changing the 1s/2s production default.
func fastCleanupBackoff(r *DockerRunner) {
	r.cleanupRetryBackoff = [2]time.Duration{time.Millisecond, time.Millisecond}
}

// registerFakeContainer inserts a container directly into the fake, bypassing
// create, so cleanup/remove can be exercised in isolation from Run.
func registerFakeContainer(r *DockerRunner, f *fakeDocker, name string) *fakeContainer {
	c := &fakeContainer{Name: name, ID: uuid.NewString()}
	c.Config.Labels = r.labels()
	f.containers[name] = c
	return c
}

func TestDockerCleanupRetrySucceedsOnThirdAttempt(t *testing.T) {
	r, f, _ := dockerFixture(t)
	fastCleanupBackoff(r)
	name := "retry-success"
	c := registerFakeContainer(r, f, name)
	c.rmFailReal = 2
	if err := r.cleanup(name); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	broken := r.cleanupFailure
	r.mu.Unlock()
	if broken != nil {
		t.Fatal("cleanupFailure set after eventual success")
	}
	if _, ok := f.containers[name]; ok {
		t.Fatal("container not removed")
	}
}

func TestDockerCleanupTreatsAmbiguousRmAsSuccessOnNextEmptyPS(t *testing.T) {
	r, f, _ := dockerFixture(t)
	fastCleanupBackoff(r)
	name := "ambiguous"
	c := registerFakeContainer(r, f, name)
	c.rmAmbiguousOnce = true
	if err := r.cleanup(name); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	broken := r.cleanupFailure
	r.mu.Unlock()
	if broken != nil {
		t.Fatal("cleanupFailure set despite the container already being gone")
	}
}

func TestDockerCleanupDefersConfirmedStoppedContainerUntilSweep(t *testing.T) {
	r, f, _ := dockerFixture(t)
	fastCleanupBackoff(r)
	name := "stopped-pending"
	c := registerFakeContainer(r, f, name)
	c.rmFailReal = 1000
	c.State.Status = "exited"
	err := r.cleanup(name)
	if !errors.Is(err, errCleanupDeferred) {
		t.Fatalf("expected errCleanupDeferred, got %v", err)
	}
	r.mu.Lock()
	broken, pending := r.cleanupFailure, len(r.pendingCleanup)
	r.mu.Unlock()
	if broken != nil {
		t.Fatal("cleanupFailure set for a confirmed-stopped container")
	}
	if pending != 1 {
		t.Fatalf("pending cleanup set = %d, want 1", pending)
	}
	// The next admission sweep, once the daemon cooperates, removes it.
	c.rmFailReal = 0
	r.sweepPendingCleanup()
	r.mu.Lock()
	pending = len(r.pendingCleanup)
	r.mu.Unlock()
	if pending != 0 {
		t.Fatal("sweep did not clear the pending container")
	}
	if _, ok := f.containers[name]; ok {
		t.Fatal("swept container not removed")
	}
}

// TestDockerCleanupFailsClosedWhenStillActive locks each of the three
// still-active State fields to its own subtest, with Status already
// "exited" so the fail-closed outcome can only come from the field under
// test - never from an otherwise-ambiguous zero-value Status that would fail
// closed on its own regardless (the same pitfall the foreman flagged for the
// W-5 confirmation-transport-failure and label-mismatch tests).
func TestDockerCleanupFailsClosedWhenStillActive(t *testing.T) {
	for _, field := range []string{"Running", "Paused", "Restarting"} {
		t.Run(field, func(t *testing.T) {
			r, f, in := dockerFixture(t)
			fastCleanupBackoff(r)
			name := "still-active-" + field
			c := registerFakeContainer(r, f, name)
			c.rmFailReal = 1000
			c.State.Status = "exited"
			switch field {
			case "Running":
				c.State.Running = true
			case "Paused":
				c.State.Paused = true
			case "Restarting":
				c.State.Restarting = true
			}
			err := r.cleanup(name)
			if err == nil || errors.Is(err, errCleanupDeferred) {
				t.Fatalf("expected fail-closed, got %v", err)
			}
			r.mu.Lock()
			broken := r.cleanupFailure
			r.mu.Unlock()
			if broken == nil {
				t.Fatalf("cleanupFailure not set for %s=true", field)
			}
			if _, e := r.Run(context.Background(), in, func(Event) error { return nil }); e == nil {
				t.Fatal("Run admitted after cleanup failure")
			}
		})
	}
}

func TestDockerCleanupFailsClosedWhenConfirmationTransportFails(t *testing.T) {
	for _, mode := range []string{"ps", "inspect"} {
		t.Run(mode, func(t *testing.T) {
			r, f, _ := dockerFixture(t)
			fastCleanupBackoff(r)
			name := "confirm-transport-" + mode
			c := registerFakeContainer(r, f, name)
			// A genuinely stopped container: fail-closed here can only come
			// from the transport failure below, not from a zero-value state
			// that would fail closed on its own regardless.
			c.State.Status = "exited"
			if mode == "ps" {
				c.psFails = true
			} else {
				c.inspectFails = true
			}
			err := r.cleanup(name)
			if err == nil || errors.Is(err, errCleanupDeferred) {
				t.Fatalf("expected fail-closed, got %v", err)
			}
			r.mu.Lock()
			broken := r.cleanupFailure
			r.mu.Unlock()
			if broken == nil {
				t.Fatal("cleanupFailure not set after a confirmation transport failure")
			}
		})
	}
}

func TestDockerCleanupFailsClosedOnLabelMismatchWithoutSendingRm(t *testing.T) {
	r, f, _ := dockerFixture(t)
	fastCleanupBackoff(r)
	name := "mismatched-owner"
	c := registerFakeContainer(r, f, name)
	// A genuinely stopped container: fail-closed here can only come from the
	// label mismatch below, not from a zero-value state that would fail
	// closed on its own regardless.
	c.State.Status = "exited"
	c.Config.Labels[ownerLabel] = "someone-else"
	before := len(f.calls)
	err := r.cleanup(name)
	if err == nil || errors.Is(err, errCleanupDeferred) {
		t.Fatalf("expected fail-closed, got %v", err)
	}
	for _, call := range f.calls[before:] {
		if call[0] == "rm" {
			t.Fatal("rm sent despite an owner label mismatch")
		}
	}
	r.mu.Lock()
	broken := r.cleanupFailure
	r.mu.Unlock()
	if broken == nil {
		t.Fatal("cleanupFailure not set on label mismatch")
	}
}

func TestDockerCleanupFailsClosedWhenPendingSetFull(t *testing.T) {
	r, f, _ := dockerFixture(t)
	fastCleanupBackoff(r)
	r.pendingCleanup = map[string]struct{}{}
	for i := 0; i < maxPendingCleanup; i++ {
		r.pendingCleanup["filler-"+strconv.Itoa(i)] = struct{}{}
	}
	name := "overflow"
	c := registerFakeContainer(r, f, name)
	c.rmFailReal = 1000
	c.State.Status = "exited"
	err := r.cleanup(name)
	if err == nil || errors.Is(err, errCleanupDeferred) {
		t.Fatalf("expected fail-closed at capacity, got %v", err)
	}
	r.mu.Lock()
	broken, pending := r.cleanupFailure, len(r.pendingCleanup)
	r.mu.Unlock()
	if broken == nil {
		t.Fatal("cleanupFailure not set once the pending set is full")
	}
	if pending != maxPendingCleanup {
		t.Fatalf("pending cleanup set changed: %d", pending)
	}
}

func TestDockerSweepPendingCleanupOnlyOneAtATime(t *testing.T) {
	r, f, _ := dockerFixture(t)
	name := "sweep-race"
	registerFakeContainer(r, f, name)
	r.pendingCleanup = map[string]struct{}{name: {}}
	release := make(chan struct{})
	started := make(chan struct{})
	var startedOnce sync.Once
	orig := r.command
	r.command = func(ctx context.Context, in io.Reader, out, diag io.Writer, args ...string) error {
		if args[0] == "rm" {
			startedOnce.Do(func() { close(started) })
			<-release
		}
		return orig(ctx, in, out, diag, args...)
	}
	done := make(chan struct{})
	go func() {
		r.sweepPendingCleanup()
		close(done)
	}()
	<-started
	r.sweepPendingCleanup() // concurrent call must skip, not wait, while a sweep is in flight
	select {
	case <-done:
		t.Fatal("first sweep already finished; the race was not exercised")
	default:
	}
	close(release)
	<-done
	if _, ok := f.containers[name]; ok {
		t.Fatal("container not removed by the sweep")
	}
}

// checkFixture builds a workspace and checks directory under the runner's
// root so hostPath mapping in runCheck succeeds, without going through Run.
func checkFixture(t *testing.T, r *DockerRunner) (workspace, checks string, check AcceptanceCheck) {
	t.Helper()
	workspace = filepath.Join(r.root, "workspaces", uuid.NewString())
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	checks = filepath.Join(r.root, "checks-"+uuid.NewString())
	if err := os.MkdirAll(checks, 0700); err != nil {
		t.Fatal(err)
	}
	return workspace, checks, AcceptanceCheck{Name: "unit", Command: []string{"/pack/checks/unit.sh"}, TimeoutSeconds: 1}
}

func TestDockerRunCheckPreservesResultWhenCleanupDeferred(t *testing.T) {
	r, f, _ := dockerFixture(t)
	fastCleanupBackoff(r)
	workspace, checks, check := checkFixture(t, r)
	f.start = func(ctx context.Context, c *fakeContainer, stdin io.Reader, out, diag io.Writer) error {
		c.rmFailReal = 1000
		c.State.Status = "exited"
		c.State.ExitCode = 0
		return nil
	}
	exit, timedOut, duration, err := r.runCheck(context.Background(), workspace, checks, check, io.Discard)
	if exit != 0 || timedOut || err != nil {
		t.Fatalf("exit=%d timedOut=%t duration=%s err=%v", exit, timedOut, duration, err)
	}
	r.mu.Lock()
	broken, pending := r.cleanupFailure, len(r.pendingCleanup)
	r.mu.Unlock()
	if broken != nil {
		t.Fatal("cleanupFailure set for a deferred, confirmed-stopped check container")
	}
	if pending != 1 {
		t.Fatalf("pending cleanup set = %d, want 1", pending)
	}
}

func TestDockerRunCheckPreservesTimeoutWhenCleanupDeferred(t *testing.T) {
	r, f, _ := dockerFixture(t)
	fastCleanupBackoff(r)
	workspace, checks, check := checkFixture(t, r)
	f.start = func(ctx context.Context, c *fakeContainer, stdin io.Reader, out, diag io.Writer) error {
		c.rmFailReal = 1000
		c.State.Status = "exited"
		<-ctx.Done()
		return ctx.Err()
	}
	exit, timedOut, duration, err := r.runCheck(context.Background(), workspace, checks, check, io.Discard)
	if exit != -1 || !timedOut || err != nil {
		t.Fatalf("exit=%d timedOut=%t duration=%s err=%v", exit, timedOut, duration, err)
	}
	r.mu.Lock()
	broken := r.cleanupFailure
	r.mu.Unlock()
	if broken != nil {
		t.Fatal("cleanupFailure set for a deferred, confirmed-stopped check container")
	}
}

func TestDockerRunCheckStillErrorsWhenCleanupFailsClosedDespiteZeroExit(t *testing.T) {
	r, f, _ := dockerFixture(t)
	fastCleanupBackoff(r)
	workspace, checks, check := checkFixture(t, r)
	f.start = func(ctx context.Context, c *fakeContainer, stdin io.Reader, out, diag io.Writer) error {
		c.rmFailReal = 1000
		c.State.Status = "exited"
		c.State.ExitCode = 0
		// runCheck's own inspect for exit code never looks at labels, so this
		// only surfaces later, inside cleanup's confirmation step.
		c.Config.Labels[ownerLabel] = "tampered"
		return nil
	}
	_, _, _, err := r.runCheck(context.Background(), workspace, checks, check, io.Discard)
	if err == nil {
		t.Fatal("expected an error when cleanup fails closed despite exit 0")
	}
	r.mu.Lock()
	broken := r.cleanupFailure
	r.mu.Unlock()
	if broken == nil {
		t.Fatal("cleanupFailure not set")
	}
}
