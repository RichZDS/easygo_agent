package main

import (
	"os"
	"path/filepath"
	"testing"

	"easygo-agent/internal/config"
)

// TestEntrypointConfigPathIsConfigsConfigYAML 保证 go run main.go 只读取 configs/config.yaml。
func TestEntrypointConfigPathIsConfigsConfigYAML(t *testing.T) {
	t.Parallel()

	if config.DefaultPath != "configs/config.yaml" {
		t.Fatalf("config.DefaultPath = %q, want configs/config.yaml", config.DefaultPath)
	}
}

// TestLoadDotEnvSetsMissingVariables 验证缺失的环境变量会从 .env 补齐。
func TestLoadDotEnvSetsMissingVariables(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("EASYGO_AGENT_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	t.Setenv("EASYGO_AGENT_API_KEY", "")
	if err := os.Unsetenv("EASYGO_AGENT_API_KEY"); err != nil {
		t.Fatalf("os.Unsetenv() error = %v", err)
	}

	if err := loadDotEnv(path); err != nil {
		t.Fatalf("loadDotEnv() error = %v", err)
	}
	if got := os.Getenv("EASYGO_AGENT_API_KEY"); got != "from-file" {
		t.Fatalf("EASYGO_AGENT_API_KEY = %q, want from-file", got)
	}
}

// TestLoadDotEnvKeepsExistingVariables 验证已存在的环境变量不会被 .env 覆盖。
func TestLoadDotEnvKeepsExistingVariables(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("EASYGO_AGENT_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	t.Setenv("EASYGO_AGENT_API_KEY", "from-shell")

	if err := loadDotEnv(path); err != nil {
		t.Fatalf("loadDotEnv() error = %v", err)
	}
	if got := os.Getenv("EASYGO_AGENT_API_KEY"); got != "from-shell" {
		t.Fatalf("EASYGO_AGENT_API_KEY = %q, want from-shell", got)
	}
}

// TestLoadDotEnvIgnoresMissingFile 验证缺少 .env 时不视为错误。
func TestLoadDotEnvIgnoresMissingFile(t *testing.T) {
	t.Parallel()

	if err := loadDotEnv(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatalf("loadDotEnv() error = %v, want nil", err)
	}
}

// TestDefaultConfigFileExists 保证 go run main.go 能找到 configs/config.yaml。
func TestDefaultConfigFileExists(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat(config.DefaultPath); err != nil {
		t.Fatalf("os.Stat(%q) error = %v", config.DefaultPath, err)
	}
}
