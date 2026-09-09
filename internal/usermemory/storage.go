package usermemory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"easygo-agent/internal/conversation"
)

// ProfileWriter materializes a read-only-friendly view of the canonical
// database profile. The hash is the stable user-profile ID and avoids using a
// raw username as a filesystem path or leaking it in directory listings.
type ProfileWriter interface {
	Write(context.Context, string, []conversation.LongTermMemory) error
}

type FileProfileWriter struct{ root string }

func NewFileProfileWriter(root string) (*FileProfileWriter, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("invalid profile storage root")
	}
	return &FileProfileWriter{root: root}, nil
}

func (w *FileProfileWriter) Write(ctx context.Context, user string, profile []conversation.LongTermMemory) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	digest := fmt.Sprintf("u-%x", sha256.Sum256([]byte(user)))
	directory := filepath.Join(w.root, digest)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	byKind := map[conversation.MemoryKind][]conversation.LongTermMemory{}
	for _, memory := range profile {
		byKind[memory.Kind] = append(byKind[memory.Kind], memory)
	}
	files := map[string][]conversation.MemoryKind{
		"Agent.md":       {conversation.MemoryKindAgent},
		"Memory.md":      {conversation.MemoryKindMemory},
		"Experiment.md":  {conversation.MemoryKindExperiment},
		"Error.md":       {conversation.MemoryKindError},
		"Preferences.md": {conversation.MemoryKindPreference},
		"Style.md":       {conversation.MemoryKindStyle},
		"Prompts.md":     {conversation.MemoryKindPrompt},
		"Constraints.md": {conversation.MemoryKindConstraint},
	}
	for name, kinds := range files {
		if err := atomicWrite(filepath.Join(directory, name), renderMemories(byKind, kinds)); err != nil {
			return err
		}
	}
	return atomicWrite(filepath.Join(directory, "Metadata.md"), renderMetadata(digest, profile))
}

func renderMemories(byKind map[conversation.MemoryKind][]conversation.LongTermMemory, kinds []conversation.MemoryKind) []byte {
	var text strings.Builder
	text.WriteString("# User long-term memory\n\n")
	for _, kind := range kinds {
		for _, memory := range byKind[kind] {
			fmt.Fprintf(&text, "- [%s] %s\n", memory.ID, memory.Content)
		}
	}
	return []byte(text.String())
}

func renderMetadata(userID string, profile []conversation.LongTermMemory) []byte {
	var text strings.Builder
	text.WriteString("# User profile metadata\n\n")
	fmt.Fprintf(&text, "- Profile ID: %s\n", userID)
	text.WriteString("- Canonical data: PostgreSQL `user_long_term_memories`\n")
	text.WriteString("\n| ID | Kind | Score fields | Last seen | Calls | Version |\n|---|---|---|---|---:|---:|\n")
	sort.Slice(profile, func(i, j int) bool { return profile[i].ProfileSlot < profile[j].ProfileSlot })
	for _, memory := range profile {
		fmt.Fprintf(&text, "| %s | %s | importance %.2f / confidence %.2f | %s | %d | %d |\n", memory.ID, memory.Kind, memory.Importance, memory.Confidence, memory.LastSeenAt.UTC().Format("2006-01-02T15:04:05Z"), memory.CallCount, memory.Version)
	}
	return []byte(text.String())
}

func atomicWrite(path string, content []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".memory-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(content)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
