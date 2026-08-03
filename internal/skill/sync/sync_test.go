package skillsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"easygo-agent/internal/config"
)

const validSkill = "---\nname: easygo-agent-skill\ndescription: EasyGo architecture reference.\n---\nRead the reference.\n"

// TestSyncRefreshesReadmeAndMirrorsSource verifies the complete startup publication path.
func TestSyncRefreshesReadmeAndMirrorsSource(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "skills")
	readmePath := filepath.Join(t.TempDir(), "README.md")
	writeSyncFile(t, readmePath, "current project readme")
	writeSyncFile(t, filepath.Join(rootDir, "builtin-src", "easygo-agent-skill", "SKILL.md"), validSkill)
	writeSyncFile(t, filepath.Join(rootDir, "builtin-src", "easygo-agent-skill", "references", "extra.md"), "extra")
	writeSyncFile(t, filepath.Join(rootDir, "builtin", "stale-skill", "SKILL.md"), "stale")

	syncer, err := New(config.Skills{RootDir: rootDir, ReadmeSrc: readmePath})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	for _, filePath := range []string{
		filepath.Join(rootDir, "builtin-src", "easygo-agent-skill", "references", "README.md"),
		filepath.Join(rootDir, "builtin", "easygo-agent-skill", "references", "README.md"),
	} {
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("os.ReadFile(%q) error = %v", filePath, err)
		}
		if string(content) != "current project readme" {
			t.Fatalf("content at %q = %q", filePath, content)
		}
	}
	if _, err := os.Stat(filepath.Join(rootDir, "builtin", "easygo-agent-skill", "references", "extra.md")); err != nil {
		t.Fatalf("mirrored extra file error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "builtin", "stale-skill")); !os.IsNotExist(err) {
		t.Fatalf("stale runtime skill error = %v, want not exist", err)
	}
}

// TestSyncFailureKeepsPreviousBuiltin verifies publication failures preserve the active tree.
func TestSyncFailureKeepsPreviousBuiltin(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "skills")
	readmePath := filepath.Join(t.TempDir(), "missing-README.md")
	writeSyncFile(t, filepath.Join(rootDir, "builtin-src", "easygo-agent-skill", "SKILL.md"), validSkill)
	oldPath := filepath.Join(rootDir, "builtin", "old-skill", "SKILL.md")
	writeSyncFile(t, oldPath, "old runtime content")

	syncer, err := New(config.Skills{RootDir: rootDir, ReadmeSrc: readmePath})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := syncer.Sync(context.Background()); err == nil {
		t.Fatal("Sync() error = nil, want missing README failure")
	}
	content, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("os.ReadFile(old builtin) error = %v", err)
	}
	if string(content) != "old runtime content" {
		t.Fatalf("old builtin content = %q", content)
	}
}

// TestSyncRejectsUnsafeBuiltinSource verifies symlinks never enter the runtime mirror.
func TestSyncRejectsUnsafeBuiltinSource(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "skills")
	readmePath := filepath.Join(t.TempDir(), "README.md")
	writeSyncFile(t, readmePath, "readme")
	skillDir := filepath.Join(rootDir, "builtin-src", "easygo-agent-skill")
	writeSyncFile(t, filepath.Join(skillDir, "SKILL.md"), validSkill)
	outsidePath := filepath.Join(t.TempDir(), "outside.md")
	writeSyncFile(t, outsidePath, "outside")
	if err := os.Symlink(outsidePath, filepath.Join(skillDir, "unsafe.md")); err != nil {
		t.Fatalf("os.Symlink() error = %v", err)
	}
	oldPath := filepath.Join(rootDir, "builtin", "old-skill", "SKILL.md")
	writeSyncFile(t, oldPath, "old")

	syncer, err := New(config.Skills{RootDir: rootDir, ReadmeSrc: readmePath})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := syncer.Sync(context.Background()); err == nil {
		t.Fatal("Sync() error = nil, want symlink rejection")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old builtin was not preserved: %v", err)
	}
}

// writeSyncFile creates a regular test file and its parent directories.
func writeSyncFile(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
}
