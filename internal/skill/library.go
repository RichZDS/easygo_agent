// Package skill loads a disk-backed skill collection. The process registers
// only the outermost catalog heuristic; specialized bodies stay on disk until
// LoadSkill is called.
package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// DefaultRoot is the working-directory folder that holds the catalog.
	DefaultRoot = "SKILL"
	catalogFile = "SKILL.md"
	collection  = "skill"
)

var skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

// Entry is the catalog row registered into the agent prompt. It is the only
// specialized-skill information available before LoadSkill.
type Entry struct {
	Name        string
	Description string
}

// Library is the on-disk skill collection. CatalogPrompt is what the agent
// sees at startup; LoadSkill is the public function that pulls one body in.
type Library struct {
	root      string
	heuristic string
	entries   []Entry
	bodies    map[string]string
}

// Open reads SKILL/SKILL.md and every SKILL/skill/<name>/SKILL.md. Unknown
// extra files are ignored so the catalog stays the only registered skill.
func Open(root string) (*Library, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" {
		return nil, errors.New("skill root cannot be empty")
	}
	catalogPath := filepath.Join(root, catalogFile)
	heuristic, err := os.ReadFile(catalogPath)
	if err != nil {
		return nil, fmt.Errorf("read skill catalog: %w", err)
	}
	lib := &Library{root: root, heuristic: string(heuristic), bodies: map[string]string{}}
	collectionDir := filepath.Join(root, collection)
	entries, err := os.ReadDir(collectionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return lib, nil
		}
		return nil, fmt.Errorf("read skill collection: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(collectionDir, entry.Name(), catalogFile))
		if readErr != nil {
			return nil, fmt.Errorf("read skill %s: %w", entry.Name(), readErr)
		}
		name, description, parseErr := parseFrontmatter(string(body))
		if parseErr != nil {
			return nil, fmt.Errorf("parse skill %s: %w", entry.Name(), parseErr)
		}
		if name != entry.Name() {
			return nil, fmt.Errorf("skill directory %q must match frontmatter name %q", entry.Name(), name)
		}
		if _, exists := lib.bodies[name]; exists {
			return nil, fmt.Errorf("duplicate skill name %q", name)
		}
		lib.entries = append(lib.entries, Entry{Name: name, Description: description})
		lib.bodies[name] = string(body)
	}
	sort.Slice(lib.entries, func(i, j int) bool { return lib.entries[i].Name < lib.entries[j].Name })
	return lib, nil
}

// CatalogPrompt is the only skill text injected at agent construction. It
// keeps the heuristic procedure and a directory of Use-when descriptions.
func (lib *Library) CatalogPrompt() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(lib.heuristic))
	b.WriteString("\n\n## Directory\n\n")
	if len(lib.entries) == 0 {
		b.WriteString("No specialized skills are installed.\n")
		return b.String()
	}
	b.WriteString("| Name | Use when |\n| --- | --- |\n")
	for _, entry := range lib.entries {
		fmt.Fprintf(&b, "| `%s` | %s |\n", entry.Name, escapeTable(entry.Description))
	}
	return b.String()
}

// LoadSkill is the public function that moves one specialized skill into the
// agent context. Names must match a catalog entry exactly.
func (lib *Library) LoadSkill(name string) (string, error) {
	if lib == nil {
		return "", errors.New("skill library is not loaded")
	}
	name = strings.TrimSpace(name)
	if !skillNamePattern.MatchString(name) {
		return "", fmt.Errorf("unknown skill %q", name)
	}
	body, ok := lib.bodies[name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q", name)
	}
	return body, nil
}

// Entries returns catalog rows in name order.
func (lib *Library) Entries() []Entry {
	return append([]Entry(nil), lib.entries...)
}

func parseFrontmatter(body string) (name, description string, err error) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if !strings.HasPrefix(trimmed, "---") {
		return "", "", errors.New("missing YAML frontmatter")
	}
	rest := strings.TrimPrefix(trimmed, "---")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", "", errors.New("unterminated YAML frontmatter")
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(strings.Trim(value, `"'`))
		switch key {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	if !skillNamePattern.MatchString(name) {
		return "", "", fmt.Errorf("invalid skill name %q", name)
	}
	if description == "" {
		return "", "", fmt.Errorf("skill %q is missing description", name)
	}
	return name, description, nil
}

func escapeTable(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "/"), "\n", " ")
}
