package workshop

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

var tasksBucket = []byte("tasks")
var eventsBucket = []byte("events")
var keysBucket = []byte("idempotency")

// Service owns an exclusive bbolt lock and a bounded set of worker goroutines.
// Namespace must come from a trusted caller, never model-generated arguments.
type Service struct {
	workerInstructions string
	packChecks         string
	mu                 sync.Mutex
	db                 *bolt.DB
	root               string
	workflows          map[string]Workflow
	runtimes           map[string]RuntimeProfile
	runner             Runner
	queue              chan string
	slots              chan struct{}
	stop               chan struct{}
	active             map[string]context.CancelFunc
	resumeScans        map[string]struct{}
	resumeQuotaScan    func(context.Context, string) error // nil uses Docker CheckDiskQuota; test seam
	wg                 sync.WaitGroup
	closed             bool
	closeOnce          sync.Once
	closeErr           error
	err                error // durability failure: reject subsequent mutations
}

// Workflows returns a name-sorted metadata catalog. Each call returns independent
// copies, including artifact paths. The catalog is shared across namespaces.
func (s *Service) Workflows() []WorkflowMetadata {
	catalog := make([]WorkflowMetadata, 0, len(s.workflows))
	for _, w := range s.workflows {
		choices := []RuntimeChoice{}
		ids := append([]string{w.Runtime}, w.AllowedRuntimes...)
		seen := map[string]bool{}
		for _, id := range ids {
			if p, ok := s.runtimes[id]; ok && !seen[id] {
				choices = append(choices, p.choice(id))
				seen[id] = true
			}
		}
		catalog = append(catalog, WorkflowMetadata{
			Runtime: w.Runtime, Runtimes: choices,
			Name: w.Name, Version: w.Version, Engine: w.Engine, Model: w.Model,
			Policy: w.Policy, TimeoutSeconds: w.TimeoutSeconds,
			Artifacts: append([]string{}, w.Artifacts...),
		})
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
	return catalog
}

// New opens storage and marks every previously unfinished attempt interrupted.
// Passing nil runner constructs the native CLI runner from Config. No unfinished
// task is automatically replayed, including tasks queued before a restart.
func New(cfg Config, runner Runner) (*Service, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.Sandbox.Mode == SandboxDocker && runner != nil {
		return nil, fmt.Errorf("%w: Docker mode cannot accept a replacement runner", ErrInvalid)
	}
	s := &Service{workflows: map[string]Workflow{}, runtimes: map[string]RuntimeProfile{}, runner: runner, queue: make(chan string, cfg.Concurrency+cfg.QueueCapacity), slots: make(chan struct{}, cfg.Concurrency+cfg.QueueCapacity), stop: make(chan struct{}), active: map[string]context.CancelFunc{}, resumeScans: map[string]struct{}{}}
	if err := s.buildCatalog(cfg); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	if cfg.Sandbox.Mode == SandboxDocker {
		err = mkdirNoSymlinks(root, 0700)
		if err == nil {
			err = noSymlinks(root)
		}
	} else {
		err = os.MkdirAll(root, 0700)
	}
	if err != nil {
		return nil, err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	s.root = root
	if s.workerInstructions, err = readWorkerInstructions(cfg.PackDir); err != nil {
		return nil, err
	}
	needsChecks := false
	for _, w := range s.workflows {
		needsChecks = needsChecks || w.Acceptance != nil
	}
	if s.packChecks, err = snapshotChecks(root, cfg.PackDir, needsChecks); err != nil {
		return nil, err
	}
	if s.runner == nil {
		cfg.Root = root
		if s.runner, err = s.newRunner(cfg); err != nil {
			return nil, err
		}
	}
	s.db, err = bolt.Open(filepath.Join(root, "workshop.db"), 0600, &bolt.Options{Timeout: 200 * time.Millisecond})
	if err != nil {
		return nil, fmt.Errorf("open workshop store: %w", err)
	}
	if docker, ok := s.runner.(*DockerRunner); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		err = docker.Initialize(ctx)
		cancel()
		if err != nil {
			_ = s.db.Close()
			return nil, fmt.Errorf("initialize Docker isolation: %w", err)
		}
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{tasksBucket, eventsBucket, keysBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return markInterrupted(tx, "service restarted; explicit resume required")
	})
	if err != nil {
		_ = s.db.Close()
		return nil, err
	}
	for i := 0; i < cfg.Concurrency; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	return s, nil
}

// validateConfig checks every operator rule that needs no filesystem, store or
// Docker access, so New fails before its first side effect. All sandbox mode
// rules live here; buildCatalog validates profiles and workflows themselves.
func validateConfig(cfg Config) error {
	if cfg.Root == "" || cfg.Concurrency < 1 || cfg.Concurrency > 128 || cfg.QueueCapacity < 0 || cfg.QueueCapacity > 10000 {
		return fmt.Errorf("%w: root and bounded concurrency/queue are required", ErrInvalid)
	}
	docker := cfg.Sandbox.Mode == SandboxDocker
	if cfg.Sandbox.Mode != "" && cfg.Sandbox.Mode != SandboxHost && !docker {
		return fmt.Errorf("%w: unknown sandbox mode", ErrInvalid)
	}
	if docker {
		root, err := filepath.Abs(cfg.Root)
		if err != nil {
			return err
		}
		if err := validateRelaySocketPath(root); err != nil {
			return err
		}
		for _, e := range cfg.Engines {
			if len(e.EnvAllowlist) != 0 {
				return fmt.Errorf("%w: Docker mode forbids environment allowlists", ErrInvalid)
			}
		}
	}
	for _, p := range cfg.RuntimeProfiles {
		if docker && p.GatewayModel == "" {
			return fmt.Errorf("%w: Docker mode requires gateway runtime profiles", ErrInvalid)
		}
	}
	for _, w := range cfg.Workflows {
		if w.Acceptance != nil && !docker {
			return fmt.Errorf("%w: acceptance requires Docker mode", ErrInvalid)
		}
		// buildCatalog resolves a non-empty Runtime into RuntimeSpec or fails.
		if docker && w.Runtime == "" {
			return fmt.Errorf("%w: Docker mode requires workflow runtime profiles", ErrInvalid)
		}
	}
	return nil
}

// buildCatalog validates the runtime profiles and resolves every workflow
// against them into s.runtimes and s.workflows, copying caller-owned slices.
func (s *Service) buildCatalog(cfg Config) error {
	for id, p := range cfg.RuntimeProfiles {
		if strings.TrimSpace(id) == "" || len(id) > 128 {
			return ErrInvalid
		}
		if err := p.validate(); err != nil {
			return err
		}
		if p.GatewayModel != "" && cfg.ModelGateway == nil {
			return fmt.Errorf("%w: model gateway required", ErrInvalid)
		}
		s.runtimes[id] = p
	}
	for _, w := range cfg.Workflows {
		w.Acceptance = cloneAcceptance(w.Acceptance)
		if w.RuntimeSpec != nil {
			return fmt.Errorf("%w: runtime_spec is reserved for task snapshots", ErrInvalid)
		}
		for _, id := range append([]string{w.Runtime}, w.AllowedRuntimes...) {
			if id != "" {
				if _, ok := s.runtimes[id]; !ok {
					return fmt.Errorf("%w: unknown runtime profile", ErrInvalid)
				}
			}
		}
		var err error
		if w, err = s.selectRuntime(w, ""); err != nil {
			return err
		}
		if err := validateWorkflow(w); err != nil {
			return err
		}
		if _, exists := s.workflows[w.Name]; exists {
			return fmt.Errorf("%w: duplicate workflow", ErrInvalid)
		}
		w.Artifacts = append([]string(nil), w.Artifacts...)
		w.AllowedRuntimes = append([]string(nil), w.AllowedRuntimes...)
		s.workflows[w.Name] = w
	}
	return nil
}

// newRunner builds the default runner for cfg.Sandbox.Mode (cfg.Root already
// resolved) and checks that it has an engine for every catalog entry.
func (s *Service) newRunner(cfg Config) (Runner, error) {
	var runner Runner
	if cfg.Sandbox.Mode == SandboxDocker {
		docker, err := NewDockerRunner(cfg)
		if err != nil {
			return nil, err
		}
		runner = docker
	} else {
		command, err := NewCommandRunner(cfg.Engines, cfg.MaxOutputBytes)
		if err != nil {
			return nil, err
		}
		command.gateway = cfg.ModelGateway
		runner = command
	}
	for _, p := range s.runtimes {
		if _, ok := cfg.Engines[p.Engine]; !ok {
			return nil, fmt.Errorf("%w: runtime engine is not configured", ErrInvalid)
		}
	}
	for _, w := range s.workflows {
		if _, ok := cfg.Engines[w.Engine]; !ok {
			return nil, fmt.Errorf("%w: workflow engine is not configured", ErrInvalid)
		}
	}
	return runner, nil
}

func validateWorkflow(w Workflow) error {
	if err := validateAcceptance(w.Acceptance); err != nil {
		return err
	}
	if w.Name == "" || w.Version == "" || w.Instructions == "" || w.Model == "" || !knownEngine(w.Engine) || (w.Policy != PolicyReadOnly && w.Policy != PolicyWorkspaceWrite) || w.TimeoutSeconds < 1 || w.TimeoutSeconds > 86400 {
		return fmt.Errorf("%w: workflow needs name/version/instructions/model, known engine, explicit policy and timeout 1..86400", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, path := range w.Artifacts {
		if !validArtifactPath(path) || seen[path] {
			return fmt.Errorf("%w: artifact must be a unique clean relative file path", ErrInvalid)
		}
		seen[path] = true
	}
	return nil
}

// validArtifactPath accepts a clean, workspace-relative file path. It is the
// single predicate for workflow configuration, downloads and the descriptor walk.
func validArtifactPath(path string) bool {
	return filepath.IsLocal(path) && path != "." && filepath.Clean(path) == path && !strings.ContainsAny(path, "\\\x00")
}

func validInput(namespace, input string) bool {
	return namespace != "" && len(namespace) <= 256 && len(input) <= 256*1024
}
func keyFor(namespace, key string) []byte { b, _ := json.Marshal([]string{namespace, key}); return b }

func (s *Service) available() error {
	if s.closed {
		return ErrClosed
	}
	if s.err != nil {
		return fmt.Errorf("store unavailable: %w", s.err)
	}
	return nil
}

func (s *Service) Submit(req SubmitRequest) (*Task, error) {
	if !validInput(req.Namespace, req.Input) || len(req.IdempotencyKey) > 256 {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(); err != nil {
		return nil, err
	}
	var previous *Task
	err := s.db.View(func(tx *bolt.Tx) error {
		if req.IdempotencyKey == "" {
			return nil
		}
		id := tx.Bucket(keysBucket).Get(keyFor(req.Namespace, req.IdempotencyKey))
		if id == nil {
			return nil
		}
		var err error
		previous, err = readTask(tx, req.Namespace, string(id))
		return err
	})
	if err != nil {
		return nil, err
	}
	if previous != nil {
		if previous.Workflow.Name != req.Workflow || previous.Input != req.Input || (req.Runtime != "" && previous.Workflow.Runtime != req.Runtime) {
			return nil, ErrConflict
		}
		return previous, nil
	}
	w, ok := s.workflows[req.Workflow]
	if !ok {
		return nil, ErrWorkflow
	}
	w, err = s.selectRuntime(w, req.Runtime)
	if err != nil {
		return nil, err
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return nil, ErrFull
	}
	w.Acceptance = cloneAcceptance(w.Acceptance)
	w.Artifacts = append([]string(nil), w.Artifacts...)
	w.AllowedRuntimes = append([]string(nil), w.AllowedRuntimes...)
	id := uuid.NewString()
	workspace := filepath.Join(s.root, "workspaces", id)
	if err := os.MkdirAll(workspace, 0700); err != nil {
		<-s.slots
		return nil, err
	}
	now := time.Now().UTC()
	task := &Task{ID: id, Namespace: req.Namespace, IdempotencyKey: req.IdempotencyKey, Workflow: w, Input: req.Input, Workspace: workspace, Status: Queued, CreatedAt: now, UpdatedAt: now, Runs: []Run{{ID: uuid.NewString(), Input: req.Input, Status: Queued, Acceptance: skippedAcceptance()}}}
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := putTask(tx, task); err != nil {
			return err
		}
		if req.IdempotencyKey != "" {
			if err := tx.Bucket(keysBucket).Put(keyFor(req.Namespace, req.IdempotencyKey), []byte(id)); err != nil {
				return err
			}
		}
		return appendEvent(tx, task, Event{Kind: EventState, Text: string(Queued)})
	})
	if err != nil {
		<-s.slots
		return nil, err
	}
	s.queue <- id
	return task, nil
}

func readTask(tx *bolt.Tx, namespace, id string) (*Task, error) {
	v := tx.Bucket(tasksBucket).Get([]byte(id))
	if v == nil {
		return nil, ErrNotFound
	}
	var task Task
	if err := json.Unmarshal(v, &task); err != nil {
		return nil, err
	}
	if task.Namespace != namespace {
		return nil, ErrNotFound
	}
	return &task, nil
}
func putTask(tx *bolt.Tx, task *Task) error {
	v, err := json.Marshal(task)
	if err != nil {
		return err
	}
	return tx.Bucket(tasksBucket).Put([]byte(task.ID), v)
}
func appendEvent(tx *bolt.Tx, task *Task, event Event) error {
	b, err := tx.Bucket(eventsBucket).CreateBucketIfNotExists([]byte(task.ID))
	if err != nil {
		return err
	}
	seq, err := b.NextSequence()
	if err != nil {
		return err
	}
	event.Sequence, event.Time, event.RunID = seq, time.Now().UTC(), task.latest().ID
	v, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], seq)
	return b.Put(key[:], v)
}

// Get, List and Events hold s.mu so a closed or failed store reports ErrClosed
// or the store error instead of a bolt error from a closed database.
func (s *Service) Get(namespace, id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(); err != nil {
		return nil, err
	}
	return s.get(namespace, id)
}

// get reads one owned task; the caller holds s.mu and has checked available().
func (s *Service) get(namespace, id string) (*Task, error) {
	var task *Task
	err := s.db.View(func(tx *bolt.Tx) error { var err error; task, err = readTask(tx, namespace, id); return err })
	return task, err
}

func (s *Service) List(namespace string) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(); err != nil {
		return nil, err
	}
	list := []Task{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(tasksBucket).ForEach(func(_, v []byte) error {
			var task Task
			if err := json.Unmarshal(v, &task); err != nil {
				return err
			}
			if task.Namespace == namespace {
				list = append(list, task)
			}
			return nil
		})
	})
	return list, err
}

// Events returns at most 1000 events after the exclusive sequence cursor.
func (s *Service) Events(namespace, id string, after uint64) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(); err != nil {
		return nil, err
	}
	events := []Event{}
	err := s.db.View(func(tx *bolt.Tx) error {
		if _, err := readTask(tx, namespace, id); err != nil {
			return err
		}
		b := tx.Bucket(eventsBucket).Bucket([]byte(id))
		if b == nil || after == ^uint64(0) {
			return nil
		}
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], after+1)
		c := b.Cursor()
		for k, v := c.Seek(key[:]); k != nil && len(events) < 1000; k, v = c.Next() {
			var event Event
			if err := json.Unmarshal(v, &event); err != nil {
				return err
			}
			events = append(events, event)
		}
		return nil
	})
	return events, err
}

func (s *Service) Cancel(namespace, id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(); err != nil {
		return nil, err
	}
	var task *Task
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		task, err = readTask(tx, namespace, id)
		if err != nil || terminal(task.Status) || task.Status == Cancelling {
			return err
		}
		if task.Status == Queued {
			if err := finishRun(tx, task, Cancelled, "cancelled before start"); err != nil {
				return err
			}
		} else {
			task.Status = Cancelling
			task.latest().Status = Cancelling
			task.UpdatedAt = time.Now().UTC()
		}
		if err := putTask(tx, task); err != nil {
			return err
		}
		return appendEvent(tx, task, Event{Kind: EventState, Text: string(task.Status)})
	})
	if err == nil {
		if cancel := s.active[id]; cancel != nil {
			cancel()
		}
	}
	return task, err
}

// maxTaskRuns caps attempts per task; Resume refuses with ErrRunLimit beyond it.
const maxTaskRuns = 256

func (s *Service) Resume(namespace, id, input string) (*Task, error) {
	if !validInput(namespace, input) {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(); err != nil {
		return nil, err
	}
	task, err := s.get(namespace, id)
	if err != nil {
		return nil, err
	}
	if len(task.Runs) >= maxTaskRuns {
		return nil, ErrRunLimit
	}
	if !terminal(task.Status) || task.SessionID == "" {
		return nil, ErrConflict
	}
	if docker, ok := s.runner.(*DockerRunner); ok {
		if _, exists := s.resumeScans[task.ID]; exists {
			return nil, ErrConflict
		}
		scanID, workspace, sessionID, runCount := task.ID, task.Workspace, task.SessionID, len(task.Runs)
		s.resumeScans[scanID] = struct{}{}
		// Also release single-flight state during panic unwinding; the inner closure
		// restores the lock before any outer defers run.
		defer delete(s.resumeScans, scanID)
		scan := docker.CheckDiskQuota
		if s.resumeQuotaScan != nil {
			scan = s.resumeQuotaScan
		}
		scanErr := func() error {
			s.mu.Unlock()
			defer s.mu.Lock()
			return s.scanResumeQuota(scan, workspace)
		}()
		delete(s.resumeScans, scanID)
		if scanErr != nil {
			return nil, scanErr
		}
		if err := s.available(); err != nil {
			return nil, err
		}
		task, err = s.get(namespace, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, ErrConflict
			}
			return nil, err
		}
		if len(task.Runs) >= maxTaskRuns {
			return nil, ErrRunLimit
		}
		if !terminal(task.Status) || task.ID != scanID || task.Workspace != workspace || task.SessionID != sessionID || len(task.Runs) != runCount {
			return nil, ErrConflict
		}
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return nil, ErrFull
	}
	task.Status, task.UpdatedAt = Queued, time.Now().UTC()
	task.Runs = append(task.Runs, Run{ID: uuid.NewString(), Input: input, ResumeSessionID: task.SessionID, Status: Queued, Acceptance: skippedAcceptance()})
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := resumeCrewInput(tx, task); err != nil {
			return err
		}
		if err := putTask(tx, task); err != nil {
			return err
		}
		return appendEvent(tx, task, Event{Kind: EventState, Text: string(Queued)})
	})
	if err != nil {
		<-s.slots
		return nil, err
	}
	s.queue <- id
	return task, nil
}

const resumeQuotaScanTimeout = 30 * time.Second

// The service stop channel is the existing shutdown authority. The watcher is
// joined before returning, including quick scans, so it cannot leak per request.
func (s *Service) scanResumeQuota(scan func(context.Context, string) error, workspace string) error {
	ctx, cancel := context.WithTimeout(context.Background(), resumeQuotaScanTimeout)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	err := scan(ctx, workspace)
	if err == nil {
		err = ctx.Err()
	}
	cancel()
	<-stopped
	return err
}

func (s *Service) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		case id := <-s.queue:
			s.execute(id)
			<-s.slots
		}
	}
}

func (s *Service) execute(id string) {
	s.mu.Lock()
	if s.closed || s.err != nil {
		s.mu.Unlock()
		return
	}
	var task Task
	err := s.db.View(func(tx *bolt.Tx) error { return json.Unmarshal(tx.Bucket(tasksBucket).Get([]byte(id)), &task) })
	if err != nil {
		s.err = err
		s.mu.Unlock()
		return
	}
	if task.Status != Queued {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(task.Workflow.TimeoutSeconds)*time.Second)
	s.active[id] = cancel
	now := time.Now().UTC()
	task.Status, task.UpdatedAt = Running, now
	run := task.latest()
	run.Status, run.StartedAt = Running, &now
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := putTask(tx, &task); err != nil {
			return err
		}
		return appendEvent(tx, &task, Event{Kind: EventState, Text: string(Running)})
	})
	if err != nil {
		s.err = err
		delete(s.active, id)
		cancel()
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	defer cancel()
	result, runErr := s.runner.Run(ctx, Invocation{Crew: runCrew{service: s, namespace: task.Namespace, taskID: task.ID, runID: run.ID}, WorkerInstructions: s.workerInstructions, Namespace: task.Namespace, Workflow: task.Workflow, Workspace: task.Workspace, Input: run.Input, SessionID: run.ResumeSessionID}, func(event Event) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		err := s.db.Update(func(tx *bolt.Tx) error {
			current, err := readTask(tx, task.Namespace, id)
			if err != nil {
				return err
			}
			if event.SessionID != "" {
				current.SessionID = event.SessionID
				current.latest().SessionID = event.SessionID
				if err := putTask(tx, current); err != nil {
					return err
				}
			}
			return appendEvent(tx, current, event)
		})
		if err != nil {
			s.err = err
		}
		return err
	})
	var artifacts []Artifact
	if runErr == nil {
		artifacts, runErr = collectArtifacts(task.Workspace, task.Workflow.Artifacts)
	}
	if runErr == nil {
		runErr = s.runAcceptance(ctx, task)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, id)
	err = s.db.Update(func(tx *bolt.Tx) error {
		current, err := readTask(tx, task.Namespace, id)
		if err != nil {
			return err
		}
		status, reason := Succeeded, ""
		switch {
		case errors.Is(runErr, ErrDiskQuotaExceeded), errors.Is(runErr, ErrDiskQuotaScanFailed):
			status, reason = Failed, runErr.Error()
		case current.Status == Cancelling:
			status, reason = Cancelled, "cancelled by caller"
		case s.closed:
			status, reason = Interrupted, "service stopped; explicit resume required"
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			status, reason = TimedOut, "workflow deadline exceeded"
		case runErr != nil:
			status, reason = Failed, runErr.Error()
		}
		if err := finishRun(tx, current, status, reason); err != nil {
			return err
		}
		r := current.latest()
		r.Text, r.Usage = result.Text, result.Usage
		if status == Succeeded {
			r.Artifacts = artifacts
		}
		if result.SessionID != "" {
			current.SessionID, r.SessionID = result.SessionID, result.SessionID
		}
		if err := putTask(tx, current); err != nil {
			return err
		}
		return appendEvent(tx, current, Event{Kind: EventState, Text: string(status)})
	})
	if err != nil {
		s.err = err
	}
}

func finish(task *Task, status Status, reason string) {
	now := time.Now().UTC()
	task.Status, task.UpdatedAt = status, now
	run := task.latest()
	previousStatus := run.Status
	run.Status, run.FinishedAt, run.Error = status, &now, reason
	run.Outcome = OutcomeNone
	if run.Acceptance == nil {
		run.Acceptance = skippedAcceptance()
	}
	if status == Interrupted && task.Workflow.Acceptance != nil && (previousStatus == Running || previousStatus == Cancelling) {
		run.Acceptance.State = AcceptanceInterrupted
	}
	if run.Acceptance.State == AcceptanceRunning {
		switch status {
		case Cancelled, TimedOut:
			run.Acceptance.State = AcceptanceCancelled
		case Interrupted:
			run.Acceptance.State = AcceptanceInterrupted
		}
	}
}

// finishRun is the only way a run becomes terminal inside a transaction: finish
// may rewrite the acceptance state, so the crew outcome and false-green flag
// are recomputed from the same events right after it.
func finishRun(tx *bolt.Tx, task *Task, status Status, reason string) error {
	finish(task, status, reason)
	return finishCrewOutcome(tx, task)
}

// markInterrupted moves every non-terminal task to Interrupted with reason and
// records its acceptance/state events. Restart (New) and stop (Close) share it;
// tasks are rewritten after the scan because bbolt forbids mutation in ForEach.
func markInterrupted(tx *bolt.Tx, reason string) error {
	var tasks []*Task
	if err := tx.Bucket(tasksBucket).ForEach(func(_, v []byte) error {
		var task Task
		if err := json.Unmarshal(v, &task); err != nil {
			return err
		}
		if !terminal(task.Status) {
			if err := finishRun(tx, &task, Interrupted, reason); err != nil {
				return err
			}
			tasks = append(tasks, &task)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, task := range tasks {
		if err := putTask(tx, task); err != nil {
			return err
		}
		if a := task.latest().Acceptance; a != nil && a.State == AcceptanceInterrupted {
			if err := appendEvent(tx, task, Event{Kind: EventAcceptance, Acceptance: &AcceptanceEvent{State: AcceptanceInterrupted, FalseGreen: a.FalseGreen}}); err != nil {
				return err
			}
		}
		if err := appendEvent(tx, task, Event{Kind: EventState, Text: string(Interrupted)}); err != nil {
			return err
		}
	}
	return nil
}

// collectArtifacts opens each path exactly as Artifact downloads do (no symlink
// anywhere on the path), so a recorded artifact is always downloadable.
func collectArtifacts(workspace string, paths []string) ([]Artifact, error) {
	artifacts := make([]Artifact, 0, len(paths))
	for _, path := range paths {
		f, err := openArtifactDownload(workspace, path)
		if err != nil {
			return nil, fmt.Errorf("artifact %q is missing, a symlink or escapes workspace", path)
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = f.Close()
			return nil, fmt.Errorf("artifact %q is not a regular file", path)
		}
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(f, 256*1024*1024+1))
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if n > 256*1024*1024 {
			return nil, fmt.Errorf("artifact %q exceeds 256 MiB", path)
		}
		artifacts = append(artifacts, Artifact{Path: path, Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))})
	}
	return artifacts, nil
}

// Close cancels running processes, joins workers, persists interruption states,
// then releases the exclusive store lock. It is safe to call more than once.
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.stop)
		for _, cancel := range s.active {
			cancel()
		}
		s.mu.Unlock()
		s.wg.Wait()
		s.closeErr = s.db.Update(func(tx *bolt.Tx) error {
			return markInterrupted(tx, "service stopped; explicit resume required")
		})
		s.closeErr = errors.Join(s.closeErr, s.err, s.db.Close())
	})
	return s.closeErr
}
