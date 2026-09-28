//go:build linux

package workshop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

type artifactRunnerFunc func(context.Context, Invocation, func(Event) error) (Result, error)

func (f artifactRunnerFunc) Run(ctx context.Context, in Invocation, emit func(Event) error) (Result, error) {
	return f(ctx, in, emit)
}

func artifactService(t *testing.T, runner Runner) *Service {
	t.Helper()
	cfg := Config{Root: t.TempDir(), Concurrency: 1, QueueCapacity: 4, Workflows: []Workflow{{Name: "download", Version: "1", Instructions: "offline fixture", Engine: "codex", Model: "fixture", Policy: "workspace-write", TimeoutSeconds: 10, Artifacts: []string{"nested/result.bin"}}}}
	s, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
func writeDownloadFixture(in Invocation, data []byte) (Result, error) {
	dir := filepath.Join(in.Workspace, "nested")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Result{}, err
	}
	err := os.WriteFile(filepath.Join(dir, "result.bin"), data, 0600)
	return Result{SessionID: fixtureSession, Text: "fixture complete"}, err
}
func completedArtifact(t *testing.T, data []byte) (*Service, *Task) {
	t.Helper()
	s := artifactService(t, artifactRunnerFunc(func(ctx context.Context, in Invocation, emit func(Event) error) (Result, error) {
		return writeDownloadFixture(in, data)
	}))
	task, err := s.Submit(SubmitRequest{Namespace: "owner", Workflow: "download", Input: "first"})
	if err != nil {
		t.Fatal(err)
	}
	task = waitTask(t, s, "owner", task.ID, func(task *Task) bool { return terminal(task.Status) })
	if task.Status != Succeeded {
		t.Fatal(task.Status, task.Runs[0].Error)
	}
	return s, task
}
func assertDownload(t *testing.T, s *Service, task *Task, runID string, data []byte) {
	t.Helper()
	out, err := s.Artifact("owner", task.ID, runID, "nested/result.bin")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(out.DataBase64)
	hash := sha256.Sum256(data)
	if err != nil || !bytes.Equal(decoded, data) || out.Path != "nested/result.bin" || out.Size != int64(len(data)) || out.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatal("download content/metadata mismatch")
	}
}

func TestArtifactDownloadOwnedRecordedFile(t *testing.T) {
	data := []byte{0, 1, 2, 255, '\n', 'x'}
	s, task := completedArtifact(t, data)
	assertDownload(t, s, task, "", data)
	assertDownload(t, s, task, task.Runs[0].ID, data)
	for _, tc := range []struct {
		ns, id, run, path string
		want              error
	}{
		{"other", task.ID, "", "nested/result.bin", ErrNotFound}, {"owner", "missing", "", "nested/result.bin", ErrNotFound}, {"owner", task.ID, "other-run", "nested/result.bin", ErrNotFound},
		{"owner", task.ID, "", "unknown.txt", ErrNotFound}, {"owner", task.ID, "", ".workshop-home/codex/auth.json", ErrNotFound},
		{"owner", task.ID, "", "../nested/result.bin", ErrInvalid}, {"owner", task.ID, "", "/etc/passwd", ErrInvalid}, {"owner", task.ID, "", "nested/../nested/result.bin", ErrInvalid}, {"owner", task.ID, "", "nested//result.bin", ErrInvalid}, {"owner", task.ID, "", "nested\\result.bin", ErrInvalid}, {"owner", task.ID, "", "nested/result.bin\x00", ErrInvalid},
	} {
		out, err := s.Artifact(tc.ns, tc.id, tc.run, tc.path)
		if out != nil || !errors.Is(err, tc.want) {
			t.Errorf("%q: out=%v err=%v want=%v", tc.path, out, err, tc.want)
		}
	}
}

func TestArtifactDownloadRejectsChangesSymlinksAndSpecialFiles(t *testing.T) {
	for _, mode := range []string{"same-size-change", "size-change", "missing", "file-symlink-outside", "file-symlink-inside", "parent-symlink", "workspace-symlink", "root-symlink", "fifo", "directory"} {
		t.Run(mode, func(t *testing.T) {
			s, task := completedArtifact(t, []byte("original"))
			file := filepath.Join(task.Workspace, "nested", "result.bin")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "same-size-change":
				must(os.WriteFile(file, []byte("modified"), 0600))
			case "size-change":
				must(os.WriteFile(file, []byte("different size"), 0600))
			case "missing":
				must(os.Remove(file))
			case "file-symlink-outside", "file-symlink-inside":
				parent := t.TempDir()
				if mode == "file-symlink-inside" {
					parent = task.Workspace
				}
				target := filepath.Join(parent, "same-bytes")
				must(os.WriteFile(target, []byte("original"), 0600))
				must(os.Remove(file))
				must(os.Symlink(target, file))
			case "parent-symlink":
				outside := t.TempDir()
				must(os.WriteFile(filepath.Join(outside, "result.bin"), []byte("original"), 0600))
				must(os.RemoveAll(filepath.Dir(file)))
				must(os.Symlink(outside, filepath.Dir(file)))
			case "workspace-symlink", "root-symlink":
				target := task.Workspace
				if mode == "root-symlink" {
					target = s.root
				}
				moved := target + "-moved"
				must(os.Rename(target, moved))
				must(os.Symlink(moved, target))
				defer func() { os.Remove(target); os.Rename(moved, target) }()
			case "fifo":
				must(os.Remove(file))
				must(syscall.Mkfifo(file, 0600))
			case "directory":
				must(os.Remove(file))
				must(os.Mkdir(file, 0700))
			}
			done := make(chan error, 1)
			go func() { _, err := s.Artifact("owner", task.ID, "", "nested/result.bin"); done <- err }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrConflict) || err.Error() != ErrConflict.Error() {
					t.Fatalf("unsanitized or accepted path: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("artifact read blocked on non-regular path")
			}
		})
	}
}

func TestArtifactDownloadEightMiBLimit(t *testing.T) {
	for _, size := range []int{0, maxArtifactDownloadBytes, maxArtifactDownloadBytes + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			data := bytes.Repeat([]byte{'a'}, size)
			s, task := completedArtifact(t, data)
			if size <= maxArtifactDownloadBytes {
				assertDownload(t, s, task, "", data)
			} else {
				out, err := s.Artifact("owner", task.ID, "", "nested/result.bin")
				if out != nil || !errors.Is(err, ErrArtifactTooLarge) {
					t.Fatalf("limit: out=%v err=%v", out, err)
				}
			}
		})
	}
}

func TestArtifactDownloadResumeSerializationAndOldHash(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := artifactService(t, artifactRunnerFunc(func(ctx context.Context, in Invocation, emit func(Event) error) (Result, error) {
		if in.Input == "resume" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}
		return writeDownloadFixture(in, []byte(in.Input))
	}))
	task, err := s.Submit(SubmitRequest{Namespace: "owner", Workflow: "download", Input: "first"})
	if err != nil {
		t.Fatal(err)
	}
	task = waitTask(t, s, "owner", task.ID, func(task *Task) bool { return task.Status == Succeeded })
	oldRun := task.Runs[0].ID
	// A racing download either completes with the original pinned bytes before
	// Resume gets the mutex, or reports conflict after Resume transitions state.
	start := make(chan struct{})
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		out, e := s.Artifact("owner", task.ID, oldRun, "nested/result.bin")
		if e == nil {
			b, _ := base64.StdEncoding.DecodeString(out.DataBase64)
			if string(b) != "first" {
				errs <- errors.New("mixed resume bytes")
				return
			}
		} else if !errors.Is(e, ErrConflict) {
			errs <- e
			return
		}
		errs <- nil
	}()
	close(start)
	if _, err = s.Resume("owner", task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	<-entered
	wg.Wait()
	if err = <-errs; err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"", oldRun} {
		if _, err = s.Artifact("owner", task.ID, runID, "nested/result.bin"); !errors.Is(err, ErrConflict) {
			t.Fatal("running task exposed artifact", err)
		}
	}
	close(release)
	task = waitTask(t, s, "owner", task.ID, func(task *Task) bool { return task.Status == Succeeded })
	if _, err = s.Artifact("owner", task.ID, oldRun, "nested/result.bin"); !errors.Is(err, ErrConflict) {
		t.Fatal("old hash returned overwritten bytes", err)
	}
	assertDownload(t, s, task, "", []byte("resume"))
}
