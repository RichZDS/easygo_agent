package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	einofs "github.com/cloudwego/eino/adk/filesystem"
)

// TestBackendReadIsUserScoped verifies that reads resolve only within the selected user's workspace.
func TestBackendReadIsUserScoped(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(userWorkspacePath(rootDir, 11), "user-skill"), "user eleven")
	createSkillFile(t, filepath.Join(userWorkspacePath(rootDir, 12), "user-skill"), "user twelve")
	backend, err := manager.NewBackend(context.Background(), 11)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}

	content, err := backend.Read(context.Background(), &einofs.ReadRequest{FilePath: "/user-skill/SKILL.md"})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if content.Content != "user eleven" {
		t.Fatalf("Read() content = %q, want %q", content.Content, "user eleven")
	}

	otherUserPath := filepath.Join(userWorkspacePath(rootDir, 12), "user-skill", "SKILL.md")
	for _, unsafePath := range []string{"../12/user-skill/SKILL.md", otherUserPath} {
		if _, err := backend.Read(context.Background(), &einofs.ReadRequest{FilePath: unsafePath}); err == nil {
			t.Errorf("Read(%q) error = nil, want rejection", unsafePath)
		}
	}
}

// TestBackendReadFollowsManagedBuiltinLink verifies that manager-created builtin links remain readable.
func TestBackendReadFollowsManagedBuiltinLink(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(rootDir, "builtin", "easygo-agent-skill"), "builtin content")
	backend, err := manager.NewBackend(context.Background(), 13)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}

	content, err := backend.Read(context.Background(), &einofs.ReadRequest{FilePath: "/easygo-agent-skill/SKILL.md"})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if content.Content != "builtin content" {
		t.Fatalf("Read() content = %q, want %q", content.Content, "builtin content")
	}
}

// TestBackendRejectsUnsafePaths verifies traversal, physical paths, and out-of-sandbox links cannot be read.
func TestBackendRejectsUnsafePaths(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(userWorkspacePath(rootDir, 14), "user-skill"), "safe")
	backend, err := manager.NewBackend(context.Background(), 14)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	outsideFile := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	workspaceDir := userWorkspacePath(rootDir, 14)
	if err := os.Symlink(outsideFile, filepath.Join(workspaceDir, "outside-link.md")); err != nil {
		t.Fatalf("os.Symlink() error = %v", err)
	}

	unsafePaths := []string{
		"..",
		"../user-skill/SKILL.md",
		filepath.Join(workspaceDir, "user-skill", "SKILL.md"),
		"/outside-link.md",
	}
	for _, unsafePath := range unsafePaths {
		if _, err := backend.Read(context.Background(), &einofs.ReadRequest{FilePath: unsafePath}); err == nil {
			t.Errorf("Read(%q) error = nil, want rejection", unsafePath)
		}
	}
}

// TestBackendRejectsArbitraryInternalSymlinks verifies that only manager-owned builtin symlinks may be followed.
func TestBackendRejectsArbitraryInternalSymlinks(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(userWorkspacePath(rootDir, 21), "user-skill"), "safe")
	backend, err := manager.NewBackend(context.Background(), 21)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	workspaceDir := userWorkspacePath(rootDir, 21)
	if err := os.Symlink(filepath.Join("user-skill", "SKILL.md"), filepath.Join(workspaceDir, "leaf-link.md")); err != nil {
		t.Fatalf("leaf os.Symlink() error = %v", err)
	}
	if err := os.Symlink("user-skill", filepath.Join(workspaceDir, "directory-link")); err != nil {
		t.Fatalf("directory os.Symlink() error = %v", err)
	}

	for _, virtualPath := range []string{"/leaf-link.md", "/directory-link/SKILL.md"} {
		if _, err := backend.Read(context.Background(), &einofs.ReadRequest{FilePath: virtualPath}); err == nil {
			t.Errorf("Read(%q) error = nil, want arbitrary symlink rejection", virtualPath)
		}
	}
}

// TestBackendGlobFindsUserAndBuiltinSkills verifies glob discovery explicitly traverses managed builtin links.
func TestBackendGlobFindsUserAndBuiltinSkills(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(rootDir, "builtin", "easygo-agent-skill"), "builtin")
	createSkillFile(t, filepath.Join(userWorkspacePath(rootDir, 15), "user-skill"), "user")
	backend, err := manager.NewBackend(context.Background(), 15)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}

	infos, err := backend.GlobInfo(context.Background(), &einofs.GlobInfoRequest{Pattern: "/*/SKILL.md", Path: "/"})
	if err != nil {
		t.Fatalf("GlobInfo() error = %v", err)
	}
	got := fileInfoPaths(infos)
	want := []string{"/easygo-agent-skill/SKILL.md", "/user-skill/SKILL.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GlobInfo() paths = %#v, want %#v", got, want)
	}
}

// TestBackendReturnsVirtualPaths verifies listing and grep results never expose physical paths.
func TestBackendReturnsVirtualPaths(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(userWorkspacePath(rootDir, 16), "user-skill"), "before\nneedle\nafter\n")
	backend, err := manager.NewBackend(context.Background(), 16)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}

	infos, err := backend.LsInfo(context.Background(), &einofs.LsInfoRequest{Path: "/"})
	if err != nil {
		t.Fatalf("LsInfo() error = %v", err)
	}
	if got := fileInfoPaths(infos); !reflect.DeepEqual(got, []string{"/user-skill"}) {
		t.Fatalf("LsInfo() paths = %#v, want %#v", got, []string{"/user-skill"})
	}

	matches, err := backend.GrepRaw(context.Background(), &einofs.GrepRequest{
		Pattern:     "needle",
		Path:        "/user-skill",
		Glob:        "*.md",
		BeforeLines: 1,
		AfterLines:  1,
	})
	if err != nil {
		t.Fatalf("GrepRaw() error = %v", err)
	}
	wantMatches := []einofs.GrepMatch{
		{Path: "/user-skill/SKILL.md", Line: 1, Content: "before"},
		{Path: "/user-skill/SKILL.md", Line: 2, Content: "needle"},
		{Path: "/user-skill/SKILL.md", Line: 3, Content: "after"},
	}
	if !reflect.DeepEqual(matches, wantMatches) {
		t.Fatalf("GrepRaw() matches = %#v, want %#v", matches, wantMatches)
	}
	for _, match := range matches {
		if strings.Contains(match.Path, rootDir) {
			t.Errorf("GrepRaw() exposed physical path %q", match.Path)
		}
	}
}

// TestBackendReadPaginatesLines verifies Eino's one-based offset and line-limit behavior.
func TestBackendReadPaginatesLines(t *testing.T) {
	manager, rootDir := newTestManager(t)
	createSkillFile(t, filepath.Join(userWorkspacePath(rootDir, 17), "user-skill"), "one\ntwo\nthree\nfour")
	backend, err := manager.NewBackend(context.Background(), 17)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}

	content, err := backend.Read(context.Background(), &einofs.ReadRequest{
		FilePath: "user-skill/SKILL.md",
		Offset:   2,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if content.Content != "two\nthree" {
		t.Fatalf("Read() content = %q, want %q", content.Content, "two\nthree")
	}
}

// TestBackendIsReadOnly verifies model-facing mutation methods consistently return the read-only sentinel.
func TestBackendIsReadOnly(t *testing.T) {
	manager, _ := newTestManager(t)
	backend, err := manager.NewBackend(context.Background(), 18)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}

	writeErr := backend.Write(context.Background(), &einofs.WriteRequest{FilePath: "/new", Content: "new"})
	if !errors.Is(writeErr, ErrReadOnly) {
		t.Fatalf("Write() error = %v, want ErrReadOnly", writeErr)
	}
	editErr := backend.Edit(context.Background(), &einofs.EditRequest{FilePath: "/new", OldString: "a", NewString: "b"})
	if !errors.Is(editErr, ErrReadOnly) {
		t.Fatalf("Edit() error = %v, want ErrReadOnly", editErr)
	}
}

// fileInfoPaths returns file information paths in their existing order.
func fileInfoPaths(infos []einofs.FileInfo) []string {
	paths := make([]string, 0, len(infos))
	for _, info := range infos {
		paths = append(paths, info.Path)
	}
	return paths
}
