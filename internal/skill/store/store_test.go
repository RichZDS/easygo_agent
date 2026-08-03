package store

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"easygo-agent/internal/config"
	"easygo-agent/internal/skill/workspace"
	einofs "github.com/cloudwego/eino/adk/filesystem"
)

// TestStoreUploadListDelete verifies the complete private skill lifecycle and deterministic metadata.
func TestStoreUploadListDelete(t *testing.T) {
	store, manager, rootDir := newTestStore(t, config.Skills{})
	writeStoreSkill(t, filepath.Join(rootDir, "builtin", "builtin-guide"), "builtin-guide", "Builtin guide")

	info, err := store.Upload(context.Background(), 11, "user-guide", bytes.NewReader(validArchive(t, "user-guide", "User guide")))
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if info.SkillID != "user-guide" || info.Source != SourceUser || info.Readonly {
		t.Fatalf("Upload() info = %#v", info)
	}

	got, err := store.List(context.Background(), 11)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := []SkillInfo{
		{SkillID: "builtin-guide", Source: SourceBuiltin, Readonly: true, Description: "Builtin guide"},
		{SkillID: "user-guide", Source: SourceUser, Readonly: false, Description: "User guide"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}

	other, err := store.List(context.Background(), 12)
	if err != nil {
		t.Fatalf("other user List() error = %v", err)
	}
	if !reflect.DeepEqual(other, want[:1]) {
		t.Fatalf("other user List() = %#v, want builtin only", other)
	}

	if err := store.Delete(context.Background(), 11, "user-guide"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	backend, err := manager.NewBackend(context.Background(), 11)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	if _, err := backend.Read(context.Background(), &einofs.ReadRequest{FilePath: "/user-guide/SKILL.md"}); err == nil {
		t.Fatal("deleted skill remains readable")
	}
}

// TestStoreRejectsConflictsAndInvalidManifests verifies naming and ownership boundaries.
func TestStoreRejectsConflictsAndInvalidManifests(t *testing.T) {
	store, _, rootDir := newTestStore(t, config.Skills{})
	writeStoreSkill(t, filepath.Join(rootDir, "builtin", "builtin-guide"), "builtin-guide", "Builtin")

	testCases := []struct {
		name      string
		skillID   string
		archive   []byte
		wantError error
	}{
		{name: "invalid ID", skillID: "Bad_ID", archive: validArchive(t, "valid-id", "Valid"), wantError: ErrInvalidSkill},
		{name: "manifest mismatch", skillID: "requested-id", archive: validArchive(t, "different-id", "Different"), wantError: ErrInvalidSkill},
		{name: "builtin conflict", skillID: "builtin-guide", archive: validArchive(t, "builtin-guide", "Conflict"), wantError: ErrConflict},
		{name: "fork skill", skillID: "fork-skill", archive: archiveWithFiles(t, []zipTestFile{{name: "SKILL.md", content: "---\nname: fork-skill\ndescription: Fork\ncontext: fork\n---\nbody"}}), wantError: ErrInvalidSkill},
	}

	for _, testCase := range testCases {
		// runInvalidUploadCase verifies one rejected store mutation.
		runInvalidUploadCase := func(t *testing.T) {
			_, err := store.Upload(context.Background(), 21, testCase.skillID, bytes.NewReader(testCase.archive))
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("Upload() error = %v, want errors.Is(%v)", err, testCase.wantError)
			}
		}
		t.Run(testCase.name, runInvalidUploadCase)
	}

	if _, err := store.Upload(context.Background(), 21, "duplicate", bytes.NewReader(validArchive(t, "duplicate", "First"))); err != nil {
		t.Fatalf("first duplicate Upload() error = %v", err)
	}
	if _, err := store.Upload(context.Background(), 21, "duplicate", bytes.NewReader(validArchive(t, "duplicate", "Second"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("second duplicate Upload() error = %v, want conflict", err)
	}
	if err := store.Delete(context.Background(), 21, "builtin-guide"); !errors.Is(err, ErrBuiltinReadOnly) {
		t.Fatalf("Delete(builtin) error = %v", err)
	}
	if err := store.Delete(context.Background(), 21, "missing-skill"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(missing) error = %v", err)
	}
}

// TestStoreEnforcesArchiveSafetyLimits verifies compressed input, extracted bytes, file count, and path safety.
func TestStoreEnforcesArchiveSafetyLimits(t *testing.T) {
	testCases := []struct {
		name      string
		cfg       config.Skills
		archive   []byte
		wantError error
	}{
		{name: "compressed bytes", cfg: config.Skills{MaxZipBytes: 16}, archive: validArchive(t, "limit-skill", "Limit"), wantError: ErrArchiveTooLarge},
		{name: "extracted bytes", cfg: config.Skills{MaxExtractedBytes: 8}, archive: validArchive(t, "limit-skill", "Limit"), wantError: ErrArchiveTooLarge},
		{name: "file count", cfg: config.Skills{MaxFiles: 1}, archive: archiveWithFiles(t, []zipTestFile{{name: "SKILL.md", content: "---\nname: limit-skill\ndescription: Limit\n---\nbody"}, {name: "references/a.md", content: "a"}}), wantError: ErrArchiveTooLarge},
		{name: "traversal", archive: archiveWithFiles(t, []zipTestFile{{name: "../SKILL.md", content: "unsafe"}}), wantError: ErrInvalidSkill},
		{name: "absolute", archive: archiveWithFiles(t, []zipTestFile{{name: "/SKILL.md", content: "unsafe"}}), wantError: ErrInvalidSkill},
		{name: "backslash", archive: archiveWithFiles(t, []zipTestFile{{name: `..\SKILL.md`, content: "unsafe"}}), wantError: ErrInvalidSkill},
		{name: "symlink", archive: archiveWithFiles(t, []zipTestFile{{name: "SKILL.md", content: "target", mode: os.ModeSymlink | 0o777}}), wantError: ErrInvalidSkill},
	}

	for _, testCase := range testCases {
		// runUnsafeArchiveCase verifies one bounded or unsafe archive rejection.
		runUnsafeArchiveCase := func(t *testing.T) {
			store, _, _ := newTestStore(t, testCase.cfg)
			_, err := store.Upload(context.Background(), 31, "limit-skill", bytes.NewReader(testCase.archive))
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("Upload() error = %v, want errors.Is(%v)", err, testCase.wantError)
			}
		}
		t.Run(testCase.name, runUnsafeArchiveCase)
	}
}

// TestStoreAcceptsUniqueTopDirectoryAndRejectsAmbiguousManifests verifies the two supported ZIP layout boundaries.
func TestStoreAcceptsUniqueTopDirectoryAndRejectsAmbiguousManifests(t *testing.T) {
	store, _, _ := newTestStore(t, config.Skills{})
	topDirectoryArchive := archiveWithFiles(t, []zipTestFile{
		{name: "bundle/", mode: os.ModeDir | 0o755},
		{name: "bundle/SKILL.md", content: "---\nname: top-skill\ndescription: Top directory\n---\nbody"},
		{name: "bundle/references/", mode: os.ModeDir | 0o755},
		{name: "bundle/references/info.md", content: "reference"},
	})
	if _, err := store.Upload(context.Background(), 35, "top-skill", bytes.NewReader(topDirectoryArchive)); err != nil {
		t.Fatalf("top-directory Upload() error = %v", err)
	}

	ambiguousArchive := archiveWithFiles(t, []zipTestFile{
		{name: "SKILL.md", content: "---\nname: ambiguous-skill\ndescription: Root\n---\nbody"},
		{name: "nested/SKILL.md", content: "---\nname: nested-skill\ndescription: Nested\n---\nbody"},
	})
	if _, err := store.Upload(context.Background(), 35, "ambiguous-skill", bytes.NewReader(ambiguousArchive)); !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("ambiguous Upload() error = %v, want invalid skill", err)
	}
}

// TestCleanupStagingRemovesOnlyOwnedEntries verifies startup cleanup cannot erase unrelated staging content.
func TestCleanupStagingRemovesOnlyOwnedEntries(t *testing.T) {
	store, manager, _ := newTestStore(t, config.Skills{})
	stagingDir, err := manager.StagingDir(context.Background())
	if err != nil {
		t.Fatalf("StagingDir() error = %v", err)
	}
	for _, name := range []string{"skill-upload-old", "skill-delete-old", "unrelated"} {
		if err := os.Mkdir(filepath.Join(stagingDir, name), 0o750); err != nil {
			t.Fatalf("os.Mkdir(%q) error = %v", name, err)
		}
	}
	if err := store.CleanupStaging(context.Background()); err != nil {
		t.Fatalf("CleanupStaging() error = %v", err)
	}
	for _, removed := range []string{"skill-upload-old", "skill-delete-old"} {
		if _, err := os.Stat(filepath.Join(stagingDir, removed)); !os.IsNotExist(err) {
			t.Errorf("owned staging %q error = %v, want not exist", removed, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stagingDir, "unrelated")); err != nil {
		t.Fatalf("unrelated staging was removed: %v", err)
	}
}

// zipTestFile describes one in-memory archive entry.
type zipTestFile struct {
	name    string
	content string
	mode    os.FileMode
}

// newTestStore creates a Store and workspace manager with isolated limits and directories.
func newTestStore(t *testing.T, overrides config.Skills) (*Store, *workspace.Manager, string) {
	t.Helper()
	rootDir := filepath.Join(t.TempDir(), "skills")
	cfg := config.Skills{
		RootDir:           rootDir,
		ReadmeSrc:         filepath.Join(t.TempDir(), "README.md"),
		MaxZipBytes:       5 << 20,
		MaxExtractedBytes: 20 << 20,
		MaxFiles:          200,
	}
	if overrides.MaxZipBytes != 0 {
		cfg.MaxZipBytes = overrides.MaxZipBytes
	}
	if overrides.MaxExtractedBytes != 0 {
		cfg.MaxExtractedBytes = overrides.MaxExtractedBytes
	}
	if overrides.MaxFiles != 0 {
		cfg.MaxFiles = overrides.MaxFiles
	}
	manager, err := workspace.NewManager(cfg)
	if err != nil {
		t.Fatalf("workspace.NewManager() error = %v", err)
	}
	store, err := New(cfg, manager)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return store, manager, rootDir
}

// validArchive creates a root-layout user skill ZIP.
func validArchive(t *testing.T, skillID, description string) []byte {
	t.Helper()
	return archiveWithFiles(t, []zipTestFile{
		{name: "SKILL.md", content: "---\nname: " + skillID + "\ndescription: " + description + "\n---\nbody"},
		{name: "references/info.md", content: "reference"},
	})
}

// archiveWithFiles builds one test ZIP from explicit entries.
func archiveWithFiles(t *testing.T, files []zipTestFile) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		if file.mode != 0 {
			header.SetMode(file.mode)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("CreateHeader() error = %v", err)
		}
		if _, err := entry.Write([]byte(file.content)); err != nil {
			t.Fatalf("zip entry Write() error = %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip Close() error = %v", err)
	}
	return buffer.Bytes()
}

// writeStoreSkill creates a valid physical skill directory for a test.
func writeStoreSkill(t *testing.T, skillDir, skillID, description string) {
	t.Helper()
	if err := os.MkdirAll(skillDir, 0o750); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	content := "---\nname: " + skillID + "\ndescription: " + description + "\n---\nbody"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
}
