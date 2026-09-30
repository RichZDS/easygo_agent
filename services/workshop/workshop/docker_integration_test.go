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

// This test is intentionally opt-in and never discovers a default/shared daemon.
// Build deploy/runtime/Dockerfile.fixture and set EASYGO_DOCKER_TEST_ENDPOINT,
// EASYGO_DOCKER_TEST_IMAGE. All gateway traffic stays on an in-process mTLS fixture.
func TestDockerIntegration(t *testing.T) {
	endpoint, image := os.Getenv("EASYGO_DOCKER_TEST_ENDPOINT"), os.Getenv("EASYGO_DOCKER_TEST_IMAGE")
	if endpoint == "" || image == "" {
		t.Skip("set explicit EASYGO_DOCKER_TEST_ENDPOINT and EASYGO_DOCKER_TEST_IMAGE for dedicated daemon proof")
	}
	root := t.TempDir()
	// Optional short root supports the AF_UNIX 108-byte path bound in long checkout paths.
	if parent := os.Getenv("EASYGO_DOCKER_TEST_ROOT"); parent != "" {
		var err error
		root, err = os.MkdirTemp(parent, "easygo-proof-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(root)
	}
	gateway := nativeRelayFixture(t, func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
		var p struct{ Namespace, Model, Protocol string }
		if json.Unmarshal(raw, &p) != nil || p.Namespace != "task-namespace" || p.Model != "fixture-model" || p.Protocol != "responses" {
			t.Error("scope escaped")
		}
		return map[string]any{"content_type": "application/json", "body": []byte(`{"fixture":"ok"}`)}, nil
	})
	cfg := Config{Root: root, ModelGateway: gateway, Sandbox: SandboxConfig{Mode: "docker", DockerBinary: os.Getenv("EASYGO_DOCKER_TEST_BINARY"), Endpoint: endpoint, Image: image, Owner: "proof-" + uuid.NewString(), HostRoot: root, DiskQuotaBytes: 16 << 20, DiskQuotaFiles: 1000, DiskPollMS: 200}}
	r, err := NewDockerRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err = r.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspaces", uuid.NewString())
	sibling := filepath.Join(root, "workspaces", uuid.NewString())
	os.MkdirAll(workspace, 0700)
	os.MkdirAll(sibling, 0700)
	sentinel := filepath.Join(root, "host-sentinel")
	os.WriteFile(sentinel, []byte("dummy-host-sentinel"), 0600)
	os.WriteFile(filepath.Join(sibling, "sentinel"), []byte("dummy-sibling-sentinel"), 0600)
	profile := RuntimeProfile{Engine: "codex", Protocol: "responses", GatewayModel: "fixture-model"}
	in := Invocation{Namespace: "task-namespace", Workspace: workspace, Workflow: Workflow{Name: "proof", Version: "1", Engine: "codex", Model: "fixture-model", Policy: "workspace-write", Instructions: "offline proof", TimeoutSeconds: 30, RuntimeSpec: &profile}}
	for _, mode := range []string{"first", "resume", "readonly"} {
		if mode == "readonly" {
			in.Workflow.Policy = "read-only"
		}
		raw, _ := json.Marshal(map[string]string{"mode": mode, "host_sentinel": sentinel, "sibling": filepath.Join(sibling, "sentinel")})
		in.Input = string(raw)
		result, e := r.Run(ctx, in, func(e Event) error {
			if e.Kind == "diagnostic" {
				t.Log(e.Text)
			}
			return nil
		})
		if e != nil {
			t.Fatal(mode, e)
		}
		in.SessionID = result.SessionID
		proofPath := filepath.Join(workspace, "isolation-proof.json")
		if mode == "readonly" {
			proofPath = filepath.Join(workspace, ".workshop-home", "isolation-proof.json")
		}
		raw, e = os.ReadFile(proofPath)
		if e != nil {
			t.Fatal(e)
		}
		var proof struct {
			Checks map[string]bool
			Limits map[string]string
		}
		if e = json.Unmarshal(raw, &proof); e != nil {
			t.Fatal(e)
		}
		for name, ok := range proof.Checks {
			if !ok {
				t.Error(name)
			}
		}
		if proof.Limits["memory"] != "1073741824" || proof.Limits["pids"] != "128" || (proof.Limits["cpu"] != "100000 100000" && proof.Limits["cpu"] != "100000") {
			t.Fatalf("limits not observed: %+v", proof.Limits)
		}
		t.Logf("%s: %s", mode, raw)
	}

	in.Workflow.Policy = "workspace-write"

	// Parent and descendant remain alive until cancellation/deadline. Removal
	// must terminate the entire container namespace in either case.
	for _, mode := range []string{"cancel", "timeout"} {
		if err := os.Remove(filepath.Join(workspace, "ready")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		in.Input = `{"mode":"sleep"}`
		runCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		done := make(chan error, 1)
		go func() { _, e := r.Run(runCtx, in, func(Event) error { return nil }); done <- e }()
		deadline := time.After(2 * time.Second)
		ticker := time.NewTicker(20 * time.Millisecond)
	ready:
		for {
			select {
			case <-ticker.C:
				if _, e := os.Stat(filepath.Join(workspace, "ready")); e == nil {
					break ready
				}
			case e := <-done:
				ticker.Stop()
				stop()
				t.Fatalf("sleep fixture exited early: %v", e)
			case <-deadline:
				ticker.Stop()
				stop()
				t.Fatal("fixture child did not start")
			}
		}
		ticker.Stop()
		ids, e := r.output(ctx, "ps", "-q", "--filter", "label="+ownerLabel+"="+r.cfg.Owner)
		if e != nil || ids == "" {
			stop()
			t.Fatal("running container missing", e)
		}
		top, e := r.output(ctx, "top", strings.Fields(ids)[0], "-eo", "pid,comm")
		if e != nil || len(strings.Split(top, "\n")) < 4 {
			stop()
			t.Fatal("descendant process missing", top, e)
		}
		if mode == "cancel" {
			stop()
		}
		select {
		case e := <-done:
			if e == nil {
				stop()
				t.Fatal("canceled/timed out run succeeded")
			}
		case <-time.After(25 * time.Second):
			stop()
			t.Fatal("cancel/deadline did not return")
		}
		stop()
		ids, e = r.output(ctx, "ps", "-aq", "--filter", "label="+ownerLabel+"="+r.cfg.Owner)
		if e != nil || ids != "" {
			t.Fatal("cancel/deadline left container/process namespace", ids, e)
		}
		t.Log(mode + ": parent and descendant container removed")
	}
	// Simulated controller crash leaves an owned container; restart must reap it,
	// while preserving a foreign container created by this test.
	orphan, foreign := "easygo-"+uuid.NewString(), "easygo-"+uuid.NewString()
	host, _ := r.hostPath(workspace)
	args := r.containerOptions(orphan, host, "", taskShimEntrypoint, false)
	args = append(args, r.cfg.Image, "codex")
	if _, e := r.output(ctx, args...); e != nil {
		t.Fatal(e)
	}
	other := &DockerRunner{cfg: r.cfg, root: r.root, gateway: r.gateway, maxOutput: r.maxOutput, command: r.command}
	other.cfg.Owner = "other-" + uuid.NewString()
	args = other.containerOptions(foreign, host, "", taskShimEntrypoint, false)
	args = append(args, r.cfg.Image, "codex")
	if _, e := other.output(ctx, args...); e != nil {
		t.Fatal(e)
	}
	defer other.cleanup(foreign)
	if e := r.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	ids, e := r.output(ctx, "ps", "-aq", "--filter", "name=^/"+orphan+"$")
	if e != nil || ids != "" {
		t.Fatal("orphan not reaped", e)
	}
	ids, e = other.output(ctx, "ps", "-aq", "--filter", "name=^/"+foreign+"$")
	if e != nil || ids == "" {
		t.Fatal("foreign container removed", e)
	}
	t.Log("verified actual containers: filesystem/network/credential boundaries, observed cgroup caps, UDS model call, cancel descendants, resume HOME/artifact, owned-only restart cleanup")
}
