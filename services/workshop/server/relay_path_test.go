package server

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"easygo-agent/rpc"
	"easygo-agent/rpc/rpctest"
	"easygo-agent/services/workshop/workshop"
)

func TestStartupPreservesSafeRelayPathDiagnostic(t *testing.T) {
	pki := rpctest.NewPKI(t)
	identity := pki.Issue("workshop", false)
	root := filepath.Join(t.TempDir(), strings.Repeat("x", 110))
	config := Config{ServerConfig: rpc.ServerConfig{TLS: identity}, Workshop: workshop.Config{Root: root, Concurrency: 1, Sandbox: workshop.SandboxConfig{Mode: "docker"}}}
	server, service, err := New(config)
	var pathError *workshop.RelaySocketPathError
	if server != nil || service != nil || !errors.As(err, &pathError) {
		t.Fatalf("configuration diagnostic hidden: %v", err)
	}
	if !strings.Contains(err.Error(), "107-byte") || !strings.Contains(err.Error(), "shorten workshop root") || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), identity.KeyFile) {
		t.Fatal("invalid startup diagnostic", err)
	}
}
