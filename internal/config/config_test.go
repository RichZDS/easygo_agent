package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadStorageAndSummaryConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `agent:
  max_steps: 6
  context_tokens: 12000
model:
  name: main
  apikey: "{MAIN_KEY}"
subagent:
  name: summary
  apikey: "{SUMMARY_KEY}"
database:
  driver: postgres
  dsn: "{TEST_DB}"
memory:
  dsn: "{MEMORY_DB}"
http:
  address: "127.0.0.1:9000"
queue:
  max_pending: 7
  max_workers: 2
  poll_interval: 10ms
  lease_ttl: 2s
`
	lookup := func(key string) (string, bool) {
		values := map[string]string{"MAIN_KEY": "main-key", "SUMMARY_KEY": "summary-key", "TEST_DB": "postgres://localhost/test", "MEMORY_DB": "postgres://localhost/memory"}
		v, ok := values[key]
		return v, ok
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SubAgent.Name != "summary" || cfg.SubAgent.APIKey != "summary-key" || cfg.Database.DSN != "postgres://localhost/test" || cfg.HTTP.Address != "127.0.0.1:9000" || cfg.Agent.ContextTokens != 12000 || cfg.Queue.MaxWorkers != 2 || cfg.Queue.MaxPending != 7 || cfg.Queue.PollInterval != 10*time.Millisecond || cfg.Queue.LeaseTTL != 2*time.Second {
		t.Fatal("config mapping failed")
	}
	for _, tc := range []struct{ name, content string }{
		{"unknown", content + "unknown: 1\n"},
		{"missing_sub_key", strings.Replace(content, "SUMMARY_KEY", "MISSING", 1)},
		{"missing_db", strings.Replace(content, "TEST_DB", "MISSING", 1)},
		{"tiny_budget", strings.Replace(content, "12000", "0", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path, lookup); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestMemoryModeAndSummaryFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("model:\n  name: test\n  apikey: '{KEY}'\ndatabase:\n  driver: memory\nsandbox:\n  enabled: false\n  auth_token: '{MISSING_CONTROLLER_TOKEN}'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, func(name string) (string, bool) {
		if name == "KEY" {
			return "key", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SubAgent != cfg.Model || cfg.Database.Driver != "memory" || cfg.Sandbox.Enabled || cfg.Sandbox.BaseURL != defaultSandboxBaseURL || cfg.Sandbox.AuthToken != "" || cfg.Sandbox.RequestTimeout != 11*time.Minute || cfg.Sandbox.MaxOutputBytes != defaultSandboxOutputBytes {
		t.Fatal("fallback mapping failed")
	}
}

func TestSandboxConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `model:
  name: test
  apikey: "{KEY}"
database:
  driver: memory
sandbox:
  enabled: true
  base_url: "http://127.0.0.1:8787/"
  auth_token: "{CONTROLLER_TOKEN}"
  request_timeout: 45s
  max_output_bytes: 32768
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	lookup := func(name string) (string, bool) {
		if name == "CONTROLLER_TOKEN" {
			return strings.Repeat("t", 64), true
		}
		return "key", true
	}
	cfg, err := Load(path, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sandbox.Enabled || cfg.Sandbox.BaseURL != "http://127.0.0.1:8787" || cfg.Sandbox.AuthToken != strings.Repeat("t", 64) || cfg.Sandbox.RequestTimeout != 45*time.Second || cfg.Sandbox.MaxOutputBytes != 32768 {
		t.Fatalf("sandbox config=%+v", cfg.Sandbox)
	}

	for _, invalid := range []string{
		strings.Replace(content, "http://127.0.0.1:8787/", "ftp://127.0.0.1", 1),
		strings.Replace(content, "http://127.0.0.1:8787/", "http://controller.example.com:8787", 1),
		strings.Replace(content, "45s", "0s", 1),
		strings.Replace(content, "32768", "1048577", 1),
	} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path, lookup); err == nil {
			t.Fatalf("invalid sandbox config accepted:\n%s", invalid)
		}
	}

	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, func(string) (string, bool) { return "short", true }); err == nil {
		t.Fatal("short sandbox auth token accepted")
	}
}
