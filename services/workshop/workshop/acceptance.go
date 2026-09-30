package workshop

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

const evidenceLimit = 1 << 20

type evidenceWriter struct {
	mu     sync.Mutex
	file   io.Writer
	total  int64
	stored int
	err    error
}

func (w *evidenceWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(p)
	w.total += int64(original)
	if remaining := evidenceLimit - w.stored; remaining > 0 && w.err == nil {
		if len(p) > remaining {
			p = p[:remaining]
		}
		n, err := w.file.Write(p)
		w.stored += n
		if err != nil {
			w.err = err
		} else if n != len(p) {
			w.err = io.ErrShortWrite
		}
	}
	// Drain excess output (and errors) to keep the subprocess from blocking.
	return original, nil
}
func (s *Service) acceptanceUpdate(namespace, id string, update func(*bolt.Tx, *Task, *Acceptance) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.Update(func(tx *bolt.Tx) error {
		task, err := readTask(tx, namespace, id)
		if err != nil {
			return err
		}
		run := task.latest()
		if run.Acceptance == nil {
			run.Acceptance = skippedAcceptance()
		}
		if err := update(tx, task, run.Acceptance); err != nil {
			return err
		}
		return putTask(tx, task)
	})
	if err != nil {
		s.err = err
	}
	return err
}
func falseGreen(outcome Outcome, tests string, state AcceptanceState) bool {
	return outcome == OutcomeSubmitted && tests == "pass" && state == AcceptanceFailed
}
func acceptanceFalseGreen(tx *bolt.Tx, task *Task, state AcceptanceState) (bool, error) {
	events, err := taskEvents(tx, task.ID)
	if err != nil {
		return false, err
	}
	outcome, tests := crewOutcome(events, task.latest().ID)
	return falseGreen(outcome, tests, state), nil
}
func (s *Service) acceptanceState(tx *bolt.Tx, task *Task, a *Acceptance, state AcceptanceState) error {
	if task.Status == Cancelling {
		state = AcceptanceCancelled
	} else if s.closed {
		state = AcceptanceInterrupted
	}
	a.State = state
	green, err := acceptanceFalseGreen(tx, task, state)
	if err != nil {
		return err
	}
	a.FalseGreen = green
	return appendEvent(tx, task, Event{Kind: EventAcceptance, Acceptance: &AcceptanceEvent{State: state, FalseGreen: green}})
}
func (s *Service) runAcceptance(ctx context.Context, task Task) error {
	if task.Workflow.Acceptance == nil {
		return nil
	}
	started := false
	if err := s.acceptanceUpdate(task.Namespace, task.ID, func(tx *bolt.Tx, current *Task, a *Acceptance) error {
		if current.Status != Running || s.closed || ctx.Err() != nil {
			return nil
		}
		a.State = AcceptanceRunning
		started = true
		return appendEvent(tx, current, Event{Kind: EventAcceptance, Acceptance: &AcceptanceEvent{State: AcceptanceRunning}})
	}); err != nil {
		return err
	}
	if !started {
		return nil
	}
	docker, ok := s.runner.(*DockerRunner)
	if !ok {
		return errors.New("acceptance requires Docker runner")
	}
	hash, hashErr := workspaceTreeHash(ctx, task.Workspace)
	if hashErr != nil {
		return s.acceptanceUpdate(task.Namespace, task.ID, func(tx *bolt.Tx, current *Task, a *Acceptance) error {
			state := AcceptanceError
			if ctx.Err() != nil {
				state = AcceptanceCancelled
			}
			return s.acceptanceState(tx, current, a, state)
		})
	}
	state := AcceptancePassed
	for _, check := range task.Workflow.Acceptance.Checks {
		if ctx.Err() != nil {
			state = AcceptanceCancelled
			break
		}
		evidence := Evidence{ID: uuid.NewString(), Check: check.Name, Command: append([]string{}, check.Command...), ExitCode: -1, WorkspaceSHA256: hash, Time: time.Now().UTC()}
		dir := filepath.Join(s.root, "evidence", task.ID)
		var file *os.File
		fileErr := mkdirNoSymlinks(dir, 0700)
		if fileErr == nil {
			file, fileErr = os.OpenFile(filepath.Join(dir, evidence.ID+".log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		}
		if fileErr != nil {
			state = AcceptanceError
			break
		}
		if err := s.acceptanceUpdate(task.Namespace, task.ID, func(tx *bolt.Tx, current *Task, a *Acceptance) error {
			return appendEvent(tx, current, Event{Kind: EventAcceptance, Acceptance: &AcceptanceEvent{State: AcceptanceRunning, Check: check.Name, EvidenceID: evidence.ID}})
		}); err != nil {
			file.Close()
			return err
		}
		output := &evidenceWriter{file: file}
		exit, timedOut, duration, checkErr := docker.runCheck(ctx, task.Workspace, s.packChecks, check, output)
		evidence.DurationMS = duration.Milliseconds()
		evidence.ExitCode = exit
		evidence.TimedOut = timedOut
		if checkErr != nil {
			diagnostic := "\nplatform: check infrastructure failed\n"
			if ctx.Err() != nil {
				diagnostic = "\nplatform: check cancelled\n"
			}
			_, _ = output.Write([]byte(diagnostic))
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		evidence.OutputBytes = output.total
		evidence.OutputTruncated = output.total > evidenceLimit
		checkState := AcceptancePassed
		switch {
		case ctx.Err() != nil:
			checkState = AcceptanceCancelled
		case checkErr != nil || output.err != nil || syncErr != nil || closeErr != nil:
			checkState = AcceptanceError
		case timedOut || exit != 0:
			checkState = AcceptanceFailed
		}
		if checkState == AcceptanceCancelled || checkState == AcceptanceError {
			state = checkState
		} else if checkState == AcceptanceFailed && state == AcceptancePassed {
			state = AcceptanceFailed
		}
		if err := s.acceptanceUpdate(task.Namespace, task.ID, func(tx *bolt.Tx, current *Task, a *Acceptance) error {
			a.Evidence = append(a.Evidence, evidence)
			return appendEvent(tx, current, Event{Kind: EventAcceptance, Acceptance: &AcceptanceEvent{State: checkState, Check: check.Name, ExitCode: &evidence.ExitCode, EvidenceID: evidence.ID}})
		}); err != nil {
			return err
		}
		if checkState == AcceptanceCancelled || checkState == AcceptanceError {
			break
		}
	}
	return s.acceptanceUpdate(task.Namespace, task.ID, func(tx *bolt.Tx, current *Task, a *Acceptance) error { return s.acceptanceState(tx, current, a, state) })
}
