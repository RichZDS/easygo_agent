package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"easygo-agent/internal/config"
	skillstore "easygo-agent/internal/skill/store"
	"easygo-agent/internal/skill/workspace"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// TestRuntimeSkillHandlersExposeOnlyReadTools verifies exact model-facing skill and filesystem capabilities.
func TestRuntimeSkillHandlersExposeOnlyReadTools(t *testing.T) {
	factory, _, rootDir, _ := newRuntimeSkillFixture(t)
	writeRuntimeSkill(t, filepath.Join(rootDir, "builtin", "builtin-guide"), "builtin-guide", "Builtin runtime guide")

	handlers, err := factory.buildSkillHandlers(context.Background(), 41)
	if err != nil {
		t.Fatalf("buildSkillHandlers() error = %v", err)
	}
	names, skillDescription := runtimeToolMetadata(t, handlers)
	want := []string{"glob", "grep", "ls", "read_file", "skill"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tool names = %#v, want %#v", names, want)
	}
	if !strings.Contains(skillDescription, "builtin-guide") {
		t.Fatalf("skill description = %q, want builtin skill", skillDescription)
	}
}

// TestRuntimeSkillsReloadOnNextBuild verifies each handler build observes the latest complete workspace catalog.
func TestRuntimeSkillsReloadOnNextBuild(t *testing.T) {
	factory, store, rootDir, _ := newRuntimeSkillFixture(t)
	writeRuntimeSkill(t, filepath.Join(rootDir, "builtin", "builtin-guide"), "builtin-guide", "Builtin runtime guide")
	ctx := context.Background()

	first, err := factory.buildSkillHandlers(ctx, 77)
	if err != nil {
		t.Fatalf("first buildSkillHandlers() error = %v", err)
	}
	_, firstDescription := runtimeToolMetadata(t, first)
	if strings.Contains(firstDescription, "user-guide") {
		t.Fatal("first catalog unexpectedly contains user skill")
	}

	if _, err := store.Upload(ctx, 77, "user-guide", bytes.NewReader(runtimeSkillArchive(t, "user-guide"))); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	second, err := factory.buildSkillHandlers(ctx, 77)
	if err != nil {
		t.Fatalf("second buildSkillHandlers() error = %v", err)
	}
	_, secondDescription := runtimeToolMetadata(t, second)
	if !strings.Contains(secondDescription, "user-guide") {
		t.Fatalf("second catalog = %q, want uploaded skill", secondDescription)
	}

	if err := store.Delete(ctx, 77, "user-guide"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	third, err := factory.buildSkillHandlers(ctx, 77)
	if err != nil {
		t.Fatalf("third buildSkillHandlers() error = %v", err)
	}
	_, thirdDescription := runtimeToolMetadata(t, third)
	if strings.Contains(thirdDescription, "user-guide") {
		t.Fatalf("third catalog = %q, deleted skill remains", thirdDescription)
	}
}

// TestRuntimeSkillHandlersRejectInvalidCatalog verifies malformed workspace skills fail before a Runner is persisted.
func TestRuntimeSkillHandlersRejectInvalidCatalog(t *testing.T) {
	factory, _, rootDir, _ := newRuntimeSkillFixture(t)
	invalidDir := filepath.Join(rootDir, "workspaces", "88", "broken-skill")
	if err := os.MkdirAll(invalidDir, 0o750); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, "SKILL.md"), []byte("not frontmatter"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	if _, err := factory.buildSkillHandlers(context.Background(), 88); err == nil {
		t.Fatal("buildSkillHandlers() error = nil, want invalid catalog failure")
	}
}

// newRuntimeSkillFixture creates a runtime factory and real user skill Store rooted in a temporary tree.
func newRuntimeSkillFixture(t *testing.T) (*RuntimeFactory, *skillstore.Store, string, config.Skills) {
	t.Helper()
	rootDir := filepath.Join(t.TempDir(), "skills")
	cfg := config.Skills{
		RootDir:           rootDir,
		ReadmeSrc:         filepath.Join(t.TempDir(), "README.md"),
		MaxZipBytes:       5 << 20,
		MaxExtractedBytes: 20 << 20,
		MaxFiles:          200,
	}
	manager, err := workspace.NewManager(cfg)
	if err != nil {
		t.Fatalf("workspace.NewManager() error = %v", err)
	}
	store, err := skillstore.New(cfg, manager)
	if err != nil {
		t.Fatalf("skillstore.New() error = %v", err)
	}
	return NewRuntimeFactory(NewRegistry(), manager), store, rootDir, cfg
}

// runtimeToolMetadata applies handlers and returns sorted tool names plus the skill tool description.
func runtimeToolMetadata(t *testing.T, handlers []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]) ([]string, string) {
	t.Helper()
	ctx := context.Background()
	runCtx := &adk.ChatModelAgentContext{}
	var err error
	for _, handler := range handlers {
		_, runCtx, err = handler.BeforeAgent(ctx, runCtx)
		if err != nil {
			t.Fatalf("BeforeAgent() error = %v", err)
		}
	}
	names := make([]string, 0, len(runCtx.Tools))
	skillDescription := ""
	for _, currentTool := range runCtx.Tools {
		info, err := currentTool.Info(ctx)
		if err != nil {
			t.Fatalf("tool.Info() error = %v", err)
		}
		names = append(names, info.Name)
		if info.Name == "skill" {
			skillDescription = info.Desc
		}
	}
	sort.Strings(names)
	return names, skillDescription
}

// writeRuntimeSkill creates a valid physical skill for runtime handler tests.
func writeRuntimeSkill(t *testing.T, skillDir, skillID, description string) {
	t.Helper()
	if err := os.MkdirAll(skillDir, 0o750); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	content := "---\nname: " + skillID + "\ndescription: " + description + "\n---\nbody"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
}

// runtimeSkillArchive builds one root-layout user skill ZIP for next-Turn tests.
func runtimeSkillArchive(t *testing.T, skillID string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("SKILL.md")
	if err != nil {
		t.Fatalf("zip.Create() error = %v", err)
	}
	content := "---\nname: " + skillID + "\ndescription: User runtime guide\n---\nbody"
	if _, err := entry.Write([]byte(content)); err != nil {
		t.Fatalf("zip entry Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip.Close() error = %v", err)
	}
	return buffer.Bytes()
}
