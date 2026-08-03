package manifest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateID verifies the exact public skill ID grammar.
func TestValidateID(t *testing.T) {
	for _, valid := range []string{"ab", "easygo-agent-skill", strings.Repeat("a", 64)} {
		if err := ValidateID(valid); err != nil {
			t.Errorf("ValidateID(%q) error = %v", valid, err)
		}
	}
	for _, invalid := range []string{"a", "Uppercase", "under_score", "../escape", strings.Repeat("a", 65)} {
		if err := ValidateID(invalid); err == nil {
			t.Errorf("ValidateID(%q) error = nil, want rejection", invalid)
		}
	}
}

// TestParseAcceptsInlineSkill verifies the supported minimal Eino manifest.
func TestParseAcceptsInlineSkill(t *testing.T) {
	got, err := Parse([]byte("---\nname: weather-guide\ndescription: Answers weather workflow questions.\n---\nRead references."))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.Name != "weather-guide" || got.Description != "Answers weather workflow questions." {
		t.Fatalf("Parse() = %#v", got)
	}
}

// TestParseRejectsInvalidManifests verifies syntax, required fields, and inline-only constraints.
func TestParseRejectsInvalidManifests(t *testing.T) {
	testCases := []struct {
		name    string
		content string
	}{
		{name: "missing opening delimiter", content: "name: valid-skill\ndescription: valid"},
		{name: "missing closing delimiter", content: "---\nname: valid-skill\ndescription: valid"},
		{name: "empty name", content: "---\nname: \"\"\ndescription: valid\n---\nbody"},
		{name: "invalid name", content: "---\nname: Bad_Name\ndescription: valid\n---\nbody"},
		{name: "empty description", content: "---\nname: valid-skill\ndescription: \"\"\n---\nbody"},
		{name: "fork context", content: "---\nname: valid-skill\ndescription: valid\ncontext: fork\n---\nbody"},
		{name: "agent override", content: "---\nname: valid-skill\ndescription: valid\nagent: specialist\n---\nbody"},
		{name: "model override", content: "---\nname: valid-skill\ndescription: valid\nmodel: other\n---\nbody"},
		{name: "unknown field", content: "---\nname: valid-skill\ndescription: valid\nunknown: value\n---\nbody"},
	}

	for _, testCase := range testCases {
		// runInvalidManifestCase verifies one invalid manifest variant.
		runInvalidManifestCase := func(t *testing.T) {
			if _, err := Parse([]byte(testCase.content)); err == nil {
				t.Fatal("Parse() error = nil, want rejection")
			}
		}
		t.Run(testCase.name, runInvalidManifestCase)
	}
}

// TestReadLoadsManifest verifies that file-backed parsing honors context cancellation and content validation.
func TestReadLoadsManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	content := []byte("---\nname: file-skill\ndescription: Loaded from disk.\n---\nbody")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	got, err := Read(context.Background(), path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got.Name != "file-skill" {
		t.Fatalf("Read() name = %q, want %q", got.Name, "file-skill")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(cancelled, path); err == nil {
		t.Fatal("Read() cancelled error = nil, want context error")
	}
}
