package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"easygo-agent/internal/skill"

	"github.com/cloudwego/eino/components/tool"
)

func TestLoadSkillToolReturnsCatalogBodyAndRejectsUnknownNames(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("SKILL.md", "---\nname: skill-catalog\ndescription: Use when deciding whether a specialized skill applies.\n---\n# Skill Catalog\n")
	write("strict-arithmetic/SKILL.md", "---\nname: strict-arithmetic\ndescription: Use when the user asks for a numeric calculation.\n---\n# Strict Arithmetic\nAlways call the calculator tool.\n")
	lib, err := skill.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	item, err := NewLoadSkill(lib)
	if err != nil {
		t.Fatal(err)
	}
	invokable, ok := item.(tool.InvokableTool)
	if !ok {
		t.Fatal("load_skill is not invokable")
	}
	info, err := invokable.Info(context.Background())
	if err != nil || info.Name != "load_skill" {
		t.Fatalf("tool info=%+v err=%v", info, err)
	}
	raw, err := invokable.InvokableRun(context.Background(), `{"name":"strict-arithmetic"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out LoadSkillOutput
	if err = json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "strict-arithmetic" || !strings.Contains(out.Content, "Always call the calculator tool") {
		t.Fatalf("output=%+v", out)
	}
	if _, err = invokable.InvokableRun(context.Background(), `{"name":"../SKILL"}`); err == nil {
		t.Fatal("path traversal was accepted")
	}
}

func TestAllToolsIncludesLoadSkillWhenLibraryIsConfigured(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: skill-catalog\ndescription: Use when deciding whether a specialized skill applies.\n---\n# Skill Catalog\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib, err := skill.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	items, err := NewAgentTool().WithSkills(lib).AllTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		info, infoErr := item.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Name == "load_skill" {
			found = true
		}
	}
	if !found {
		t.Fatal("load_skill was not registered")
	}
}
