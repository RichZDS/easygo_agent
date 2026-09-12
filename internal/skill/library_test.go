package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenReadsHeuristicCatalogAndLeavesBodiesOnDisk(t *testing.T) {
	root := writeLibrary(t, map[string]string{
		"SKILL.md": `---
name: skill-catalog
description: Use when deciding whether a specialized skill applies.
---
# Skill Catalog
Call load_skill before specialized procedures.
`,
		"skill/strict-arithmetic/SKILL.md": `---
name: strict-arithmetic
description: Use when the user asks for a numeric calculation.
---
# Strict Arithmetic
Always call the calculator tool.
`,
		"skill/bracket-token-reply/SKILL.md": `---
name: bracket-token-reply
description: Use when the user asks to repeat a passphrase or token.
---
# Bracket Token Reply
Wrap the token in [[ ]].
`,
	})
	lib, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	prompt := lib.CatalogPrompt()
	if !strings.Contains(prompt, "Call load_skill") {
		t.Fatalf("catalog missing heuristic body: %s", prompt)
	}
	if !strings.Contains(prompt, "strict-arithmetic") || !strings.Contains(prompt, "numeric calculation") {
		t.Fatalf("catalog missing skill directory: %s", prompt)
	}
	if strings.Contains(prompt, "Always call the calculator tool") || strings.Contains(prompt, "Wrap the token") {
		t.Fatal("catalog leaked a specialized skill body")
	}
}

func TestLoadSkillReturnsExactBodyAndRejectsTraversal(t *testing.T) {
	root := writeLibrary(t, map[string]string{
		"SKILL.md": `---
name: skill-catalog
description: Use when deciding whether a specialized skill applies.
---
# Skill Catalog
`,
		"skill/strict-arithmetic/SKILL.md": `---
name: strict-arithmetic
description: Use when the user asks for a numeric calculation.
---
# Strict Arithmetic
Always call the calculator tool.
`,
	})
	lib, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	body, err := lib.LoadSkill("strict-arithmetic")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Always call the calculator tool") {
		t.Fatalf("loaded wrong body: %s", body)
	}
	for _, name := range []string{"missing", "../SKILL", "skill/strict-arithmetic", `..\..\windows`, ""} {
		if _, err = lib.LoadSkill(name); err == nil {
			t.Fatalf("accepted invalid skill name %q", name)
		}
	}
}

func TestRepositoryCatalogListsCollectionWithoutBodies(t *testing.T) {
	lib, err := Open(filepath.Join("..", "..", "SKILL"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := lib.CatalogPrompt()
	for _, name := range []string{"strict-arithmetic", "bracket-token-reply", "tool-error-first", "playing-maze"} {
		if !strings.Contains(prompt, name) {
			t.Fatalf("catalog missing %s: %s", name, prompt)
		}
	}
	if strings.Contains(prompt, "CALC:") || strings.Contains(prompt, "[[TOKEN]]") || strings.Contains(prompt, "Tool error:") {
		t.Fatal("repository catalog leaked specialized skill bodies")
	}
	body, err := lib.LoadSkill("strict-arithmetic")
	if err != nil || !strings.Contains(body, "CALC:") {
		t.Fatalf("load_skill body=%q err=%v", body, err)
	}
}

func writeLibrary(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
