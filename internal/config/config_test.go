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
  apikey: "{EASYGO_AGENT_API_KEY}"
tracing:
  enabled: false
  exporter: stdout
`

// TestDefaultPathIsConfigsConfigYAML 保证运行时只读取 configs/config.yaml。
func TestDefaultPathIsConfigsConfigYAML(t *testing.T) {
	t.Parallel()

	if DefaultPath != "configs/config.yaml" {
		t.Fatalf("DefaultPath = %q, want configs/config.yaml", DefaultPath)
	}
}

// TestLoadValidConfig 验证严格 YAML 加载，以及通过 {ENV} 引用注入 API Key。
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

// TestLoadRejectsInvalidConfig 验证运行所需字段的校验，不包含权限策略。
func TestLoadRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		yaml      string
		lookupEnv func(string) (string, bool)
	}{
		{name: "unknown field", yaml: validConfigYAML + "unknown: true\n", lookupEnv: testLookupKey},
		{name: "plaintext apikey", yaml: strings.Replace(validConfigYAML, `  apikey: "{EASYGO_AGENT_API_KEY}"`, "  apikey: committed-secret", 1), lookupEnv: testLookupKey},
		{name: "missing model", yaml: strings.Replace(validConfigYAML, "  name: test-model", "  name: \"\"", 1), lookupEnv: testLookupKey},
		{name: "missing api key env", yaml: validConfigYAML, lookupEnv: missingLookupKey},
		{name: "malformed base url", yaml: strings.Replace(validConfigYAML, "https://example.com/v1", "://bad", 1), lookupEnv: testLookupKey},
		{name: "zero timeout", yaml: strings.Replace(validConfigYAML, "  timeout: 2m", "  timeout: 0s", 1), lookupEnv: testLookupKey},
		{name: "zero max steps", yaml: strings.Replace(validConfigYAML, "  max_steps: 8", "  max_steps: 0", 1), lookupEnv: testLookupKey},
		{name: "unsupported tracing exporter", yaml: strings.Replace(validConfigYAML, "  exporter: stdout", "  exporter: vendor", 1), lookupEnv: testLookupKey},
		{name: "multiple yaml documents", yaml: validConfigYAML + "---\nagent: {}\n", lookupEnv: testLookupKey},
	}

	for _, test := range tests {
		test := test
		// runCase 校验一种非法配置场景。
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := writeConfig(t, test.yaml)
			if _, err := Load(path, test.lookupEnv, zap.NewNop()); err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
		})
	}
}

// testLookupKey 返回配置测试使用的确定性 API Key。
func testLookupKey(key string) (string, bool) {
	if key != APIKeyEnvironmentVariable {
		return "", false
	}
	return "test-key", true
}

// missingLookupKey 表示对应环境变量不存在。
func missingLookupKey(string) (string, bool) {
	return "", false
}

// writeConfig 以私有文件权限写入一份测试配置。
func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}
