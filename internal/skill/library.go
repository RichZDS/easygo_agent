// Package skill loads a disk-backed skill collection. The process registers
// only the outermost catalog heuristic; specialized bodies stay on disk until
// LoadSkill is called.
package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"unicode/utf8"

	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// DefaultRoot is the working-directory folder that holds the catalog.
	DefaultRoot = "skills"
	catalogFile = "SKILL.md"
)

var skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

// Entry is the catalog row registered into the agent prompt. It is the only
// specialized-skill information available before LoadSkill.
type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Library is the on-disk skill collection. CatalogPrompt is what the agent
// sees at startup; LoadSkill is the public function that pulls one body in.
type Library struct {
	root      string
	heuristic string
	entries   []Entry
	bodies    map[string]string
}

// Open reads skills/SKILL.md and every skills/<name>/SKILL.md. Directories
// without SKILL.md are ignored so staging folders can sit beside the catalog.
func Open(root string) (*Library, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("skill root cannot be empty")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	catalogPath := filepath.Join(root, catalogFile)
	heuristic, err := os.ReadFile(catalogPath)
	if err != nil {
		return nil, fmt.Errorf("read skill catalog: %w", err)
	}
	lib := &Library{root: root, heuristic: string(heuristic), bodies: map[string]string{}}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read skill collection: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(root, entry.Name(), catalogFile))
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
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
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), &meta); err != nil {
		return "", "", fmt.Errorf("invalid YAML: %w", err)
	}
	name, description = strings.TrimSpace(meta.Name), strings.TrimSpace(meta.Description)
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

// List can rediscover the entire catalog after context compression.
func (lib *Library) List(query string) []Entry {
	result := []Entry{}
	for _, entry := range lib.entries {
		if strings.TrimSpace(query) == "" || strings.Contains(strings.ToLower(entry.Name+" "+entry.Description), strings.ToLower(strings.TrimSpace(query))) {
			result = append(result, entry)
		}
	}
	return result
}

const MaxResourceBytes = 64 << 10

// ReadResource uses os.Root so containment is enforced during the actual open,
// including symlink races, rather than only checking the path beforehand.
func (lib *Library) ReadResource(name, path string) (string, error) {
	if _, err := lib.LoadSkill(name); err != nil {
		return "", err
	}
	if !filepath.IsLocal(path) {
		return "", errors.New("resource path must stay inside its skill directory")
	}
	collection, err := os.OpenRoot(lib.root)
	if err != nil {
		return "", err
	}
	defer collection.Close()
	root, err := collection.OpenRoot(name)
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.Open(path)
	if err != nil {
		return "", fmt.Errorf("open skill resource: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("skill resource must be a regular text file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxResourceBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxResourceBytes {
		return "", errors.New("skill resource exceeds 64 KiB")
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", errors.New("skill resource must be UTF-8 text")
	}
	return string(data), nil
}

// Version includes references as well as entrypoints; changed workflows block recovery.
func (lib *Library) Version() (string, error) {
	h := sha256.New()
	for _, entry := range lib.entries {
		_, _ = io.WriteString(h, entry.Name+lib.bodies[entry.Name])
		err := filepath.WalkDir(filepath.Join(lib.root, entry.Name), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(filepath.Join(lib.root, entry.Name), path)
			if err != nil {
				return err
			}
			content, err := lib.ReadResource(entry.Name, rel)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(h, rel+"\x00"+content)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
