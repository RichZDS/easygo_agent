package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
)

// TestRunBuildsTemplateWithoutNetwork 验证组装流程能到达注入的 TUI runner。
func TestRunBuildsTemplateWithoutNetwork(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`agent:
  system_prompt: test
  max_steps: 8
model:
  name: test-model
  base_url: https://example.com/v1
  timeout: 2m
  apikey: "{EASYGO_AGENT_API_KEY}"
tracing:
  enabled: false
  exporter: stdout
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	err := run(context.Background(), configPath, testLookupEnv, successfulProgramRunner)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

// testLookupEnv 返回组装测试使用的凭证。
func testLookupEnv(key string) (string, bool) {
	if key != "EASYGO_AGENT_API_KEY" {
		return "", false
	}
	return "test-key", true
}

// successfulProgramRunner 接受已组装的 Bubble Tea 程序，但不打开真实终端。
func successfulProgramRunner(*tea.Program, *zap.Logger) (tea.Model, error) {
	return nil, nil
}
