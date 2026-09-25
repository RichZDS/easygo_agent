package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadWorkshopConnection(t *testing.T) {
	const base = "model:\n  name: fixture\n  apikey: '{MODEL_KEY}'\ndatabase:\n  driver: memory\nworkshop:\n  enabled: true\n  base_url: http://127.0.0.1:8091\n  auth_token: '{WORKSHOP_TOKEN}'\n"
	lookup := func(name string) (string, bool) {
		if name == "MODEL_KEY" || name == "WORKSHOP_TOKEN" {
			return "fixture-token", true
		}
		return "", false
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Workshop.Enabled || cfg.Workshop.AuthToken != "fixture-token" || cfg.Workshop.RequestTimeout != 15*time.Second {
		t.Fatalf("workshop connection not loaded")
	}
	for _, text := range []string{
		strings.Replace(base, "'{WORKSHOP_TOKEN}'", "'inline-secret'", 1),
		strings.Replace(base, "http://127.0.0.1:8091", "http://user:secret@127.0.0.1:8091", 1),
		base + "  request_timeout: -1s\n",
	} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path, lookup); err == nil {
			t.Fatal("invalid workshop connection accepted")
		}
	}
}
