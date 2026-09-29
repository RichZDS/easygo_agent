package workshop

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func readWorkerInstructions(packDir string) (string, error) {
	if packDir == "" {
		return "", nil
	}
	root, err := os.OpenRoot(packDir)
	if err != nil {
		return "", fmt.Errorf("%w: cannot open pack directory", ErrInvalid)
	}
	defer root.Close()
	f, err := root.Open(filepath.Join("roles", "worker.md"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: cannot read worker instructions", ErrInvalid)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: worker instructions must be a regular file", ErrInvalid)
	}
	raw, err := io.ReadAll(io.LimitReader(f, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 || !utf8.Valid(raw) {
		return "", fmt.Errorf("%w: worker instructions must be UTF-8, at most 16 KiB", ErrInvalid)
	}
	return string(raw), nil
}
func invocationPrompt(in Invocation) string {
	prompt := in.Workflow.Instructions
	if in.WorkerInstructions != "" {
		prompt = in.WorkerInstructions + "\n\n" + prompt
	}
	if in.Workflow.Acceptance != nil {
		prompt += "\n\nAcceptance checks the platform will run after you finish:"
		for _, check := range in.Workflow.Acceptance.Checks {
			prompt += "\n- " + check.Name + ": " + strings.Join(check.Command, " ")
		}
	}
	return prompt + "\n\nUser input:\n" + in.Input
}
