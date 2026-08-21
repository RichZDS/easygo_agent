package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

const validConfigYAML = `agent:
  system_prompt: test assistant
  max_steps: 8
model:
  name: test-model
  base_url: https://example.com/v1
  timeout: 2m
tracing:
  enabled: false
  exporter: stdout
`

// TestLoadValidConfig verifies strict YAML loading and environment-only API Key injection.
func TestLoadValidConfig(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, validConfigYAML)
	got, err := Load(path, testLookupKey, zap.NewNop())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.Agent.SystemPrompt != "test assistant" {
		t.Errorf("Agent.SystemPrompt = %q", got.Agent.SystemPrompt)
	}
	if got.Agent.MaxSteps != 8 {
		t.Errorf("Agent.MaxSteps = %d", got.Agent.MaxSteps)
	}
	if got.Model.Name != "test-model" {
		t.Errorf("Model.Name = %q", got.Model.Name)
	}
	if got.Model.BaseURL != "https://example.com/v1" {
		t.Errorf("Model.BaseURL = %q", got.Model.BaseURL)
	}
	if got.Model.Timeout != 2*time.Minute {
		t.Errorf("Model.Timeout = %s", got.Model.Timeout)
	}
	if got.Model.APIKey != "test-key" {
		t.Errorf("Model.APIKey was not injected from the environment")
	}
	if got.Tracing.Enabled || got.Tracing.Exporter != "stdout" {
		t.Errorf("Tracing = %#v", got.Tracing)
	}
}

// TestLoadRejectsInvalidConfig verifies operational validation without permission policy.
func TestLoadRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		yaml      string
		lookupEnv func(string) (string, bool)
	}{
		{name: "unknown field", yaml: validConfigYAML + "unknown: true\n", lookupEnv: testLookupKey},
		{name: "api key in yaml", yaml: strings.Replace(validConfigYAML, "  timeout: 2m\n", "  timeout: 2m\n  api_key: committed-secret\n", 1), lookupEnv: testLookupKey},
		{name: "missing model", yaml: strings.Replace(validConfigYAML, "  name: test-model", "  name: \"\"", 1), lookupEnv: testLookupKey},
		{name: "missing api key", yaml: validConfigYAML, lookupEnv: missingLookupKey},
		{name: "malformed base url", yaml: strings.Replace(validConfigYAML, "https://example.com/v1", "://bad", 1), lookupEnv: testLookupKey},
		{name: "zero timeout", yaml: strings.Replace(validConfigYAML, "  timeout: 2m", "  timeout: 0s", 1), lookupEnv: testLookupKey},
		{name: "zero max steps", yaml: strings.Replace(validConfigYAML, "  max_steps: 8", "  max_steps: 0", 1), lookupEnv: testLookupKey},
		{name: "unsupported tracing exporter", yaml: strings.Replace(validConfigYAML, "  exporter: stdout", "  exporter: vendor", 1), lookupEnv: testLookupKey},
		{name: "multiple yaml documents", yaml: validConfigYAML + "---\nagent: {}\n", lookupEnv: testLookupKey},
	}

	for _, test := range tests {
		test := test
		// runCase verifies one invalid configuration scenario.
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := writeConfig(t, test.yaml)
			if _, err := Load(path, test.lookupEnv, zap.NewNop()); err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
		})
	}
}

// testLookupKey returns a deterministic API Key for configuration tests.
func testLookupKey(key string) (string, bool) {
	if key != APIKeyEnvironmentVariable {
		return "", false
	}
	return "test-key", true
}

// missingLookupKey reports that no environment variable exists.
func missingLookupKey(string) (string, bool) {
	return "", false
}

// writeConfig writes one test configuration with private file permissions.
func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}
