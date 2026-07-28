package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"easygo-agent/internal/config"
)

// TestEnsureWorkspaceCreatesUserDirectory verifies that a private user workspace is created.
func TestEnsureWorkspaceCreatesUserDirectory(t *testing.T) {
	manager, rootDir := newTestManager(t)

	workspaceDir, err := manager.EnsureWorkspace(context.Background(), 42)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}

	want := filepath.Join(rootDir, "workspaces", "42")
	if workspaceDir != want {
		t.Fatalf("EnsureWorkspace() = %q, want %q", workspaceDir, want)
	}
	info, err := os.Stat(workspaceDir)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("workspace is not a directory")
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("workspace mode = %o, want 750", info.Mode().Perm())
	}
}

// TestEnsureWorkspaceCreatesRelativeBuiltinLinks verifies that each builtin directory is linked by a relative target.
func TestEnsureWorkspaceCreatesRelativeBuiltinLinks(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(rootDir, "builtin", "easygo-agent-skill"), "builtin")
	createSkillFile(t, filepath.Join(rootDir, "builtin", "another-skill"), "another")
	if err := os.WriteFile(filepath.Join(rootDir, "builtin", "not-a-directory"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	workspaceDir, err := manager.EnsureWorkspace(context.Background(), 7)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}

	for _, skillID := range []string{"another-skill", "easygo-agent-skill"} {
		linkTarget, err := os.Readlink(filepath.Join(workspaceDir, skillID))
		if err != nil {
			t.Fatalf("os.Readlink(%q) error = %v", skillID, err)
		}
		want := filepath.Join("..", "..", "builtin", skillID)
		if linkTarget != want {
			t.Errorf("link target for %q = %q, want %q", skillID, linkTarget, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(workspaceDir, "not-a-directory")); !os.IsNotExist(err) {
		t.Fatalf("non-directory builtin link exists, error = %v", err)
	}
}

// TestEnsureWorkspaceIsIdempotent verifies that a correct managed link is preserved across repeated calls.
func TestEnsureWorkspaceIsIdempotent(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(rootDir, "builtin", "builtin-skill"), "builtin")

	workspaceDir, err := manager.EnsureWorkspace(context.Background(), 8)
	if err != nil {
		t.Fatalf("first EnsureWorkspace() error = %v", err)
	}
	linkPath := filepath.Join(workspaceDir, "builtin-skill")
	before, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("first os.Lstat() error = %v", err)
	}

	if _, err := manager.EnsureWorkspace(context.Background(), 8); err != nil {
		t.Fatalf("second EnsureWorkspace() error = %v", err)
	}
	after, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("second os.Lstat() error = %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatalf("correct managed symlink was replaced")
	}
}

// TestEnsureWorkspaceRepairsManagedSymlink verifies that a broken or wrong link at a builtin ID is repaired.
func TestEnsureWorkspaceRepairsManagedSymlink(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(rootDir, "builtin", "builtin-skill"), "builtin")
	workspaceDir := filepath.Join(rootDir, "workspaces", "9")
	if err := os.MkdirAll(workspaceDir, 0o750); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	linkPath := filepath.Join(workspaceDir, "builtin-skill")
	if err := os.Symlink(filepath.Join("..", "..", "missing"), linkPath); err != nil {
		t.Fatalf("os.Symlink() error = %v", err)
	}

	if _, err := manager.EnsureWorkspace(context.Background(), 9); err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("os.Readlink() error = %v", err)
	}
	want := filepath.Join("..", "..", "builtin", "builtin-skill")
	if got != want {
		t.Fatalf("repaired link target = %q, want %q", got, want)
	}
}

// TestEnsureWorkspaceRejectsBuiltinDirectoryConflict verifies that user-owned content is never removed to install a builtin link.
func TestEnsureWorkspaceRejectsBuiltinDirectoryConflict(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(rootDir, "builtin", "builtin-skill"), "builtin")
	userSkillDir := filepath.Join(rootDir, "workspaces", "10", "builtin-skill")
	createSkillFile(t, userSkillDir, "user-owned")

	if _, err := manager.EnsureWorkspace(context.Background(), 10); err == nil {
		t.Fatalf("EnsureWorkspace() error = nil, want conflict error")
	}

	content, err := os.ReadFile(filepath.Join(userSkillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	if string(content) != "user-owned" {
		t.Fatalf("user content = %q, want %q", content, "user-owned")
	}
}

// TestEnsureWorkspaceRejectsUserDirectorySymlink verifies that one user's workspace cannot alias another user's directory.
func TestEnsureWorkspaceRejectsUserDirectorySymlink(t *testing.T) {
	manager, rootDir := newTestManager(t)
	otherWorkspace := userWorkspacePath(rootDir, 20)
	createSkillFile(t, filepath.Join(otherWorkspace, "other-skill"), "other user")
	linkPath := userWorkspacePath(rootDir, 19)
	if err := os.Symlink(otherWorkspace, linkPath); err != nil {
		t.Fatalf("os.Symlink() error = %v", err)
	}

	if _, err := manager.EnsureWorkspace(context.Background(), 19); err == nil {
		t.Fatalf("EnsureWorkspace() error = nil, want workspace symlink rejection")
	}
}

// TestBuiltinIDsReturnsImmediateDirectories verifies that only immediate builtin directories are exposed as IDs.
func TestBuiltinIDsReturnsImmediateDirectories(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(rootDir, "builtin", "first"), "first")
	createSkillFile(t, filepath.Join(rootDir, "builtin", "second"), "second")
	if err := os.WriteFile(filepath.Join(rootDir, "builtin", "file"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	ids, err := manager.BuiltinIDs(context.Background())
	if err != nil {
		t.Fatalf("BuiltinIDs() error = %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("BuiltinIDs() count = %d, want 2", len(ids))
	}
	for _, skillID := range []string{"first", "second"} {
		if _, ok := ids[skillID]; !ok {
			t.Errorf("BuiltinIDs() missing %q", skillID)
		}
	}
}

// TestBuiltinIDsRejectsInvalidNames verifies that builtin directory names must match the exact skill ID grammar.
func TestBuiltinIDsRejectsInvalidNames(t *testing.T) {
	invalidNames := []string{
		"Uppercase",
		"a",
		strings.Repeat("a", 65),
		"under_score",
		"dotted.name",
	}

	for _, invalidName := range invalidNames {
		manager, rootDir := newTestManager(t)
		createSkillFile(t, filepath.Join(rootDir, "builtin", invalidName), "invalid")

		if _, err := manager.BuiltinIDs(context.Background()); err == nil {
			t.Errorf("BuiltinIDs() with %q error = nil, want invalid ID error", invalidName)
		}
	}
}

// newTestManager creates a manager rooted in an isolated temporary directory.
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()

	rootDir := filepath.Join(t.TempDir(), "skills")
	manager, err := NewManager(config.Skills{RootDir: rootDir})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager, rootDir
}

// createSkillFile creates a skill directory containing one SKILL.md file.
func createSkillFile(t *testing.T, skillDir, content string) {
	t.Helper()

	if err := os.MkdirAll(skillDir, 0o750); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", skillDir, err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", skillDir, err)
	}
}

// userWorkspacePath returns the physical workspace directory for a user ID.
func userWorkspacePath(rootDir string, userID uint64) string {
	return filepath.Join(rootDir, "workspaces", strconv.FormatUint(userID, 10))
}
