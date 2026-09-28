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
	"strings"
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
	root := t.TempDir()
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

func TestDockerOptionsAndPathMapping(t *testing.T) {
	r, _, in := dockerFixture(t)
	r.cfg.HostRoot = "/daemon/workshop"
	mapped, e := r.hostPath(in.Workspace)
	if e != nil || mapped != "/daemon/workshop/workspaces/"+filepath.Base(in.Workspace) {
		t.Fatalf("mapping %s %v", mapped, e)
	}
	args := r.containerOptions("run", mapped, "/daemon/workshop/relays/run")
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
