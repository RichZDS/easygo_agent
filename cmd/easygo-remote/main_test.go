package main

import (
	"go/build"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRPCAllowsOnlyPersonalMemoryAndSkills(t *testing.T) {
	for _, method := range []string{
		"agent.memory.list", "agent.memory.upsert", "agent.memory.delete", "agent.memory.consolidate", "agent.memory.import",
		"agent.skills.list", "agent.skills.get", "agent.skills.upsert", "agent.skills.delete", "agent.skills.import",
	} {
		if err := checkRPC(method); err != nil {
			t.Errorf("%s rejected: %v", method, err)
		}
	}
	for _, method := range []string{
		"", "agent.memory.", "agent.memory.purge", "agent.memory.list.all", "agent.skills.export", " agent.memory.list", "AGENT.MEMORY.LIST",
		"agent.session.list", "agent.run.start", "agent.workshop.catalog", "workshop.submit", "platform.wallet.reserve", "admin.credits",
	} {
		if err := checkRPC(method); err == nil {
			t.Errorf("%q accepted", method)
		}
	}
}

// The CLI must keep building after the legacy local app is removed, so its
// packages (and their tests) may import only these easygo-agent packages.
func TestCLIDependsOnlyOnClientPackages(t *testing.T) {
	allowed := []string{"easygo-agent/internal/clientapi", "easygo-agent/internal/remotetui", "easygo-agent/internal/tui"}
	seen := map[string]bool{}
	var visit func(dir string)
	visit = func(dir string) {
		pkg, err := build.ImportDir(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range slices.Concat(pkg.Imports, pkg.TestImports, pkg.XTestImports) {
			if !strings.HasPrefix(path, "easygo-agent/") || seen[path] {
				continue
			}
			seen[path] = true
			if !slices.Contains(allowed, path) {
				t.Errorf("%s imports %s", dir, path)
				continue
			}
			visit(filepath.Join("..", "..", strings.TrimPrefix(path, "easygo-agent/")))
		}
	}
	visit(".")
	if len(seen) != len(allowed) {
		t.Errorf("walked %v, want %v", seen, allowed)
	}
}
