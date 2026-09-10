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
http:
  address: "127.0.0.1:9000"
queue:
  max_pending: 7
  max_workers: 2
  poll_interval: 10ms
  lease_ttl: 2s
`
	lookup := func(key string) (string, bool) {
		values := map[string]string{"MAIN_KEY": "main-key", "SUMMARY_KEY": "summary-key", "TEST_DB": "postgres://localhost/test"}
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
	if err := os.WriteFile(path, []byte("model:\n  name: test\n  apikey: '{KEY}'\ndatabase:\n  driver: memory\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, func(string) (string, bool) { return "key", true })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SubAgent != cfg.Model || cfg.Database.Driver != "memory" {
		t.Fatal("fallback mapping failed")
	}
}
