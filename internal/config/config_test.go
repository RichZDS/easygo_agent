package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSkillsDefaults verifies that omitted skills configuration receives the documented defaults.
func TestLoadSkillsDefaults(t *testing.T) {
	path := writeConfigFile(t, "")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Skills.RootDir != "skills" {
		t.Errorf("Skills.RootDir = %q, want %q", cfg.Skills.RootDir, "skills")
	}
	if cfg.Skills.SyncCron != "0 0 3 * * *" {
		t.Errorf("Skills.SyncCron = %q, want %q", cfg.Skills.SyncCron, "0 0 3 * * *")
	}
	if cfg.Skills.ReadmeSrc != "README.md" {
		t.Errorf("Skills.ReadmeSrc = %q, want %q", cfg.Skills.ReadmeSrc, "README.md")
	}
	if cfg.Skills.MaxZipBytes != 5<<20 {
		t.Errorf("Skills.MaxZipBytes = %d, want %d", cfg.Skills.MaxZipBytes, 5<<20)
	}
	if cfg.Skills.MaxExtractedBytes != 20<<20 {
		t.Errorf("Skills.MaxExtractedBytes = %d, want %d", cfg.Skills.MaxExtractedBytes, 20<<20)
	}
	if cfg.Skills.MaxFiles != 200 {
		t.Errorf("Skills.MaxFiles = %d, want %d", cfg.Skills.MaxFiles, 200)
	}
}

// TestSkillsValidate verifies that explicit invalid skills values are rejected after defaults are applied.
func TestSkillsValidate(t *testing.T) {
	testCases := []struct {
		name   string
		skills string
	}{
		{name: "empty root directory", skills: "  root_dir: \"\"\n"},
		{name: "empty sync cron", skills: "  sync_cron: \"\"\n"},
		{name: "empty readme source", skills: "  readme_src: \"\"\n"},
		{name: "zero zip limit", skills: "  max_zip_bytes: 0\n"},
		{name: "negative zip limit", skills: "  max_zip_bytes: -1\n"},
		{name: "zero extracted limit", skills: "  max_extracted_bytes: 0\n"},
		{name: "negative extracted limit", skills: "  max_extracted_bytes: -1\n"},
		{name: "zero file limit", skills: "  max_files: 0\n"},
		{name: "negative file limit", skills: "  max_files: -1\n"},
	}

	for _, testCase := range testCases {
		path := writeConfigFile(t, "skills:\n"+testCase.skills)

		_, err := Load(path)
		if err == nil {
			t.Errorf("%s: Load() error = nil, want validation error", testCase.name)
		}
	}
}

// writeConfigFile writes a minimal valid configuration plus the supplied skills YAML.
func writeConfigFile(t *testing.T, skillsYAML string) string {
	t.Helper()

	content := strings.TrimSpace(`
mysql:
  host: localhost
  port: 3306
  user: test
  password: test
  database: test
  charset: utf8mb4
  max_idle_conns: 1
  max_open_conns: 2
  conn_max_lifetime: 60

logger:
  environment: development
  level: info
  file: test.log
  max_size_mb: 1
  max_backups: 1
  max_age_days: 1
  compress: false
`) + "\n" + skillsYAML
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}
