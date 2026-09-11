package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAssembleOwnsConfiguredMemoryStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("model:\n  name: fake-model\n  base_url: http://127.0.0.1\n  apikey: '{FAKE_KEY}'\ndatabase:\n  driver: memory\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldLookup := lookupEnv
	lookupEnv = func(string) (string, bool) { return "fake-key", true }
	t.Cleanup(func() { lookupEnv = oldLookup })

	app, err := assemble(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if app.cfg.Database.Driver != "memory" || app.store == nil {
		t.Fatalf("assembled application=%+v", app)
	}
	if _, ok := app.store.(interface{ SetMaxPendingRuns(int) }); !ok {
		t.Fatal("configured store does not expose queue capacity")
	}
	if app.memoryStore == nil {
		t.Fatal("assembled application dropped long-term memory store")
	}
	app.close()
}
