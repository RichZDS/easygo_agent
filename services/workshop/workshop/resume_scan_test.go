//go:build linux

package workshop

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type resumeResult struct {
	task *Task
	err  error
}

// No large tree or actual Docker calls: only the expensive scan is blocked.
// Native execution is independently covered by the existing Docker suite.
func resumeScanFixture(t *testing.T) (*Service, *Task, *DockerRunner) {
	t.Helper()
	s, task := completedArtifact(t, []byte("small fixture"))
	r, _, _ := dockerFixture(t)
	r.root = s.root
	r.cfg.HostRoot = s.root
	s.mu.Lock()
	s.runner = r
	s.mu.Unlock()
	return s, task, r
}
func blockedResumeScan(t *testing.T, s *Service, scanErr error) (<-chan context.Context, func(), *atomic.Int32) {
	t.Helper()
	entered := make(chan context.Context, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	var once atomic.Bool
	unlock := func() {
		if once.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(unlock)
	s.mu.Lock()
	s.resumeQuotaScan = func(ctx context.Context, workspace string) error {
		calls.Add(1)
		entered <- ctx
		select {
		case <-release:
			return scanErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Unlock()
	return entered, unlock, &calls
}
func resumeAsync(s *Service, task *Task) <-chan resumeResult {
	out := make(chan resumeResult, 1)
	go func() { task, err := s.Resume(task.Namespace, task.ID, "resume"); out <- resumeResult{task, err} }()
	return out
}
func awaitScan(t *testing.T, entered <-chan context.Context) context.Context {
	t.Helper()
	select {
	case ctx := <-entered:
		return ctx
	case <-time.After(time.Second):
		t.Fatal("resume scan did not start")
		return nil
	}
}
func awaitResume(t *testing.T, out <-chan resumeResult) resumeResult {
	t.Helper()
	select {
	case r := <-out:
		return r
	case <-time.After(time.Second):
		t.Fatal("Resume did not return")
		return resumeResult{}
	}
}
func assertScanReleased(t *testing.T, s *Service) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.resumeScans) != 0 {
		t.Fatal("single-flight entry leaked")
	}
}

func TestResumeScanDoesNotBlockOtherNamespaceControl(t *testing.T) {
	s, task, _ := resumeScanFixture(t)
	other := *task
	other.ID = uuid.NewString()
	other.Namespace = "other-owner"
	other.Workspace = filepath.Join(s.root, "workspaces", other.ID)
	other.Status = Running
	other.Runs = []Run{{ID: uuid.NewString(), Status: Running}}
	otherCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	err := s.db.Update(func(tx *bolt.Tx) error { return putTask(tx, &other) })
	s.active[other.ID] = cancel
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	entered, release, _ := blockedResumeScan(t, s, nil)
	resumed := resumeAsync(s, task)
	scanCtx := awaitScan(t, entered)
	deadline, ok := scanCtx.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining > 30*time.Second || remaining < 29*time.Second {
		t.Fatal("resume scan deadline is not30 seconds", remaining)
	}
	for _, operation := range []string{"get", "cancel"} {
		done := make(chan error, 1)
		started := time.Now()
		go func() {
			var e error
			if operation == "get" {
				_, e = s.Get(other.Namespace, other.ID)
			} else {
				_, e = s.Cancel(other.Namespace, other.ID)
			}
			done <- e
		}()
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
			elapsed := time.Since(started)
			if elapsed >= 50*time.Millisecond {
				t.Fatalf("%s took %v", operation, elapsed)
			}
			t.Logf("other-namespace %s during blocked scan: %v", operation, elapsed)
		case <-time.After(50 * time.Millisecond):
			t.Fatalf("%s blocked behind resume scan", operation)
		}
	}
	if otherCtx.Err() != context.Canceled {
		t.Fatal("other task cancel signal not delivered")
	}
	release()
	result := awaitResume(t, resumed)
	if result.err != nil {
		t.Fatal(result.err)
	}
	assertScanReleased(t, s)
}

func TestResumeScanSingleFlightQueuesExactlyOneRun(t *testing.T) {
	s, task, _ := resumeScanFixture(t)
	entered, release, calls := blockedResumeScan(t, s, nil)
	first := resumeAsync(s, task)
	awaitScan(t, entered)
	second := awaitResume(t, resumeAsync(s, task))
	if !errors.Is(second.err, ErrConflict) || second.task != nil {
		t.Fatal("second scan was admitted", second.err)
	}
	if calls.Load() != 1 {
		t.Fatal("multiple scans started")
	}
	release()
	one := awaitResume(t, first)
	if one.err != nil || len(one.task.Runs) != len(task.Runs)+1 {
		t.Fatal("first resume failed", one.err)
	}
	current, err := s.Get(task.Namespace, task.ID)
	if err != nil || len(current.Runs) != len(task.Runs)+1 {
		t.Fatal("duplicate run appended", err)
	}
	assertScanReleased(t, s)
}

func TestResumeScanRevalidatesChangedTask(t *testing.T) {
	for _, change := range []string{"session", "runs", "status", "workspace", "removed"} {
		t.Run(change, func(t *testing.T) {
			s, task, _ := resumeScanFixture(t)
			entered, release, _ := blockedResumeScan(t, s, nil)
			out := resumeAsync(s, task)
			awaitScan(t, entered)
			s.mu.Lock()
			err := s.db.Update(func(tx *bolt.Tx) error {
				current, e := readTask(tx, task.Namespace, task.ID)
				if e != nil {
					return e
				}
				switch change {
				case "session":
					current.SessionID = uuid.NewString()
				case "runs":
					current.Runs = append(current.Runs, Run{ID: uuid.NewString(), Status: Failed})
				case "status":
					current.Status = Running
				case "workspace":
					current.Workspace += "-changed"
				case "removed":
					return tx.Bucket(tasksBucket).Delete([]byte(task.ID))
				}
				return putTask(tx, current)
			})
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			release()
			result := awaitResume(t, out)
			if result.task != nil || !errors.Is(result.err, ErrConflict) {
				t.Fatalf("changed %s task resumed: %v", change, result.err)
			}
			if len(s.queue) != 0 || len(s.slots) != 0 {
				t.Fatal("changed task entered queue")
			}
			assertScanReleased(t, s)
		})
	}
}

func TestResumeScanReturnsQuotaErrorWithoutContainer(t *testing.T) {
	s, task, r := resumeScanFixture(t)
	var calls atomic.Int32
	r.command = func(context.Context, io.Reader, io.Writer, io.Writer, ...string) error {
		calls.Add(1)
		return errors.New("unexpected Docker call")
	}
	quota := &DiskQuotaError{DiskUsage: DiskUsage{UsedBytes: 17000000, UsedFiles: 100}, LimitBytes: 16777216, LimitFiles: 1000}
	entered, release, _ := blockedResumeScan(t, s, quota)
	out := resumeAsync(s, task)
	awaitScan(t, entered)
	release()
	got := awaitResume(t, out)
	if got.task != nil || got.err != quota || calls.Load() != 0 {
		t.Fatal("quota error changed or container created", got.err)
	}
	current, err := s.Get(task.Namespace, task.ID)
	if err != nil || len(current.Runs) != len(task.Runs) || current.Status != task.Status {
		t.Fatal("quota rejection mutated task")
	}
	assertScanReleased(t, s)
}

func TestResumeScanCanceledByServiceClose(t *testing.T) {
	s, task, _ := resumeScanFixture(t)
	entered, _, _ := blockedResumeScan(t, s, nil)
	out := resumeAsync(s, task)
	scanCtx := awaitScan(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	result := awaitResume(t, out)
	if result.task != nil || !errors.Is(result.err, context.Canceled) || scanCtx.Err() != context.Canceled {
		t.Fatal("close did not cancel scan", result.err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked on resume scan")
	}
	assertScanReleased(t, s)
}

func TestResumeHostModeDoesNotInvokeQuotaHook(t *testing.T) {
	s, task := completedArtifact(t, []byte("host fixture"))
	s.resumeQuotaScan = func(context.Context, string) error {
		t.Error("host path invoked quota scan")
		return ErrDiskQuotaScanFailed
	}
	if _, err := s.Resume(task.Namespace, task.ID, "host resume"); err != nil {
		t.Fatal(err)
	}
	waitTask(t, s, task.Namespace, task.ID, func(task *Task) bool { return terminal(task.Status) })
	assertScanReleased(t, s)
}
