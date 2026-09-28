package workshop

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func sizedRelayRoot(t *testing.T, socketBytes int, unicode bool) string {
	t.Helper()
	parent, err := os.MkdirTemp("/tmp", "relay-path-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })
	tail := len(filepath.Join("/", relayDirectory, relayRunPrefix+relayMaxRandomSuffix, relaySocketName))
	rootBytes := socketBytes - tail
	prefix := ""
	if unicode {
		prefix = "界"
	}
	padding := rootBytes - len(parent) - 1 - len(prefix)
	if padding < 0 {
		t.Fatal("test parent path exceeds desired boundary")
	}
	return filepath.Join(parent, prefix+strings.Repeat("x", padding))
}
func pathRunnerConfig(root string) Config {
	return Config{Root: root, Concurrency: 1, Sandbox: SandboxConfig{Mode: "docker", DockerBinary: "true", Image: "fixture", Owner: "path-proof", Endpoint: "unix:///tmp/unused-docker.sock", HostRoot: root}}
}

func TestRelaySocketStartupByteBoundary(t *testing.T) {
	for _, unicode := range []bool{false, true} {
		for _, length := range []int{107, 108} {
			label := "ascii"
			if unicode {
				label = "utf8"
			}
			if length == 108 {
				label += "-over"
			}
			t.Run(label, func(t *testing.T) {
				root := sizedRelayRoot(t, length, unicode)
				if unicode && utf8.RuneCountInString(root) == len(root) {
					t.Fatal("fixture is not multi-byte")
				}
				cfg := pathRunnerConfig(root)
				if length == 107 {
					if err := os.MkdirAll(root, 0700); err != nil {
						t.Fatal(err)
					}
					if _, err := NewDockerRunner(cfg); err != nil {
						t.Fatal("boundary root rejected", err)
					}
					socket := filepath.Join(root, relayDirectory, relayRunPrefix+relayMaxRandomSuffix, relaySocketName)
					if len(socket) != 107 {
						t.Fatal("test boundary mismatch", len(socket))
					}
					// Prove the worst-case filename itself binds on the deployment kernel.
					if runtime.GOOS == "linux" {
						if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
							t.Fatal(err)
						}
						listener, err := net.Listen("unix", socket)
						if err != nil {
							t.Fatal("107-byte socket cannot bind", err)
						}
						listener.Close()
					}
				} else {
					for _, construct := range []func() error{func() error { _, err := NewDockerRunner(cfg); return err }, func() error { _, err := New(cfg, nil); return err }} {
						err := construct()
						var pathError *RelaySocketPathError
						if !errors.As(err, &pathError) || pathError.ActualBytes != 108 || !errors.Is(err, ErrInvalid) {
							t.Fatalf("bad startup error: %v", err)
						}
						if !strings.Contains(err.Error(), "107-byte") || !strings.Contains(err.Error(), "108 bytes") || !strings.Contains(err.Error(), "shorten workshop root") || strings.Contains(err.Error(), root) {
							t.Fatal("unsafe/unhelpful diagnostic", err)
						}
					}
					if _, err := os.Stat(root); !os.IsNotExist(err) {
						t.Fatal("invalid startup created state directory", err)
					}
				}
			})
		}
	}
}

func TestRelaySocketChecksAbsoluteRootAndPreservesHostMode(t *testing.T) {
	root := sizedRelayRoot(t, 108, false)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := pathRunnerConfig(relative)
	_, err = New(cfg, nil)
	var pathError *RelaySocketPathError
	if !errors.As(err, &pathError) || pathError.ActualBytes != 108 {
		t.Fatal("relative path not measured as absolute", err)
	}
	cfg = Config{Root: root, Concurrency: 1, Sandbox: SandboxConfig{Mode: "host"}}
	service, err := New(cfg, nil)
	if err != nil {
		t.Fatal("unmanaged host root rejected", err)
	}
	service.Close()
}

func TestRelayContainerPathAndHostMapping(t *testing.T) {
	socket := filepath.Join(runtimeRelay, relaySocketName)
	if socket != "/run/easygo-relay/model.sock" || len(socket) > relaySocketPathLimit {
		t.Fatal("container socket path changed or exceeds limit")
	}
	t.Logf("container socket path is %d bytes", len(socket))
	root := sizedRelayRoot(t, 107, false)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := pathRunnerConfig(root)
	cfg.Sandbox.HostRoot = "/daemon/" + strings.Repeat("x", 150)
	if _, err := NewDockerRunner(cfg); err != nil {
		t.Fatal("daemon mount source is not a socket address", err)
	}
}
