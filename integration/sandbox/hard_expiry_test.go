package sandboxintegration_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	controllersandbox "easygo-agent/internal/sandbox"
)

const runtimeImageEnvironment = "EASYGO_SANDBOX_RUNTIME_IMAGE"
const expiryNamespaceEnvironment = "EASYGO_SANDBOX_EXPIRY_NAMESPACE"

// TestDockerEngineHardExpiryRemovesContainerAndVolume uses the real Engine
// adapter but a manually advanced lifecycle clock. Merely running `go test`
// never starts Docker: the same explicit integration URL gate as the HTTP
// suite is required first.
func TestDockerEngineHardExpiryRemovesContainerAndVolume(t *testing.T) {
	if strings.TrimSpace(os.Getenv(integrationURLEnvironment)) == "" {
		t.Skipf("set %s to run Docker sandbox integration tests", integrationURLEnvironment)
	}
	dockerSocket := strings.TrimSpace(os.Getenv("DOCKER_SOCKET_PATH"))
	if dockerSocket == "" {
		dockerSocket = "/var/run/docker.sock"
	}
	dockerHost := dockerSocket
	if !strings.Contains(dockerHost, "://") {
		dockerHost = "unix://" + dockerHost
	}
	engine, err := controllersandbox.NewDockerEngine(dockerHost)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := controllersandbox.OpenBoltStore(statePath)
	if err != nil {
		_ = engine.Close()
		t.Fatal(err)
	}
	clock := &expiryClock{now: time.Now().UTC()}
	image := strings.TrimSpace(os.Getenv(runtimeImageEnvironment))
	if image == "" {
		image = "easygo-agent-sandbox-runtime:local"
	}
	namespace := strings.TrimSpace(os.Getenv(expiryNamespaceEnvironment))
	if namespace == "" {
		namespace = envOrDefault(namespaceEnvironment, "easygo-agent-it") + "-expiry"
	}
	cfg := controllersandbox.Config{
		Namespace:           namespace,
		ListenAddress:       "127.0.0.1:8787",
		AuthToken:           "integration-controller-token-0123456789abcdef",
		StatePath:           statePath,
		DockerHost:          dockerHost,
		Image:               image,
		MaxRunning:          1,
		MaxApplications:     1,
		MaxStarting:         1,
		WaitTimeout:         30 * time.Second,
		BaseIdleTTL:         time.Minute,
		HardTTL:             5 * time.Minute,
		ReaperInterval:      time.Second,
		CPUs:                1,
		MemoryBytes:         2 << 30,
		PIDsLimit:           256,
		ShmSizeBytes:        64 << 20,
		TmpSizeBytes:        256 << 20,
		WorkspaceSoftLimit:  1 << 30,
		TotalWorkspaceLimit: 20 << 30,
		MinFreeDisk:         1,
		MaxOutputBytes:      64 << 10,
		CommandTimeout:      2 * time.Minute,
		MaxCommandTimeout:   10 * time.Minute,
	}
	manager, err := controllersandbox.NewManager(cfg, engine, store, clock)
	if err != nil {
		_ = store.Close()
		_ = engine.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := manager.Cleanup(cleanupContext); err != nil {
			t.Errorf("cleanup hard-expiry integration namespace: %v", err)
		}
		if err := manager.Close(); err != nil {
			t.Errorf("close hard-expiry manager: %v", err)
		}
		if err := engine.Close(); err != nil {
			t.Errorf("close Docker Engine client: %v", err)
		}
	})

	if err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	identity := controllersandbox.Identity{SessionID: "expiry-session-" + randomHex(t, 8), RunID: "expiry-run-1"}
	applied, err := manager.Apply(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(t.Context(), identity, applied.Application.ID); err != nil {
		t.Fatal(err)
	}
	assertManagedResourceCounts(t, engine, namespace, 1, 1)

	clock.Advance(5 * time.Minute)
	if err := manager.Reap(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertManagedResourceCounts(t, engine, namespace, 0, 0)
}

func assertManagedResourceCounts(t *testing.T, engine *controllersandbox.DockerEngine, namespace string, containers, volumes int) {
	t.Helper()
	resources, err := engine.ListManaged(t.Context(), namespace)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Containers) != containers || len(resources.Volumes) != volumes {
		t.Fatalf("managed resources: containers=%d volumes=%d, want %d/%d", len(resources.Containers), len(resources.Volumes), containers, volumes)
	}
}

type expiryClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *expiryClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (*expiryClock) NewTimer(delay time.Duration) controllersandbox.Timer {
	return &expiryTimer{timer: time.NewTimer(delay)}
}

type expiryTimer struct {
	timer *time.Timer
}

func (timer *expiryTimer) Channel() <-chan time.Time { return timer.timer.C }
func (timer *expiryTimer) Stop() bool                { return timer.timer.Stop() }

func (clock *expiryClock) Advance(delay time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delay)
	clock.mu.Unlock()
}
