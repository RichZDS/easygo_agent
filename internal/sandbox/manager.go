package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	LabelManaged       = "io.easygo.sandbox.managed"
	LabelNamespace     = "io.easygo.sandbox.namespace"
	LabelApplicationID = "io.easygo.sandbox.application_id"
	LabelResource      = "io.easygo.sandbox.resource"
	LabelHardExpiresAt = "io.easygo.sandbox.hard_expires_at"
	LabelRuntimeImage  = "io.easygo.sandbox.runtime"

	managedLabel      = LabelManaged
	namespaceLabel    = LabelNamespace
	applicationLabel  = LabelApplicationID
	resourceLabel     = LabelResource
	hardExpiryLabel   = LabelHardExpiresAt
	runtimeImageLabel = LabelRuntimeImage

	maxCommandBytes = 32 * 1024
	maxPathBytes    = 4096
	maxStdinBytes   = 1024 * 1024

	cleanupOperationTimeout = 10 * time.Second
	applyProvisionTimeout   = 30 * time.Second
	storageMonitorInterval  = time.Second
)

var (
	persistedApplicationIDPattern = regexp.MustCompile(`^app_[A-Za-z0-9_-]{32}$`)
	dockerContainerIDPattern      = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type slotWaiter struct {
	applicationID string
}

// Manager is the controller's deep module. It owns identity isolation,
// admission, lifecycle, persistence, and recovery behind one interface.
type Manager struct {
	cfg    Config
	engine Engine
	store  StateStore
	clock  Clock

	mu                sync.Mutex
	storageMu         sync.Mutex
	applications      map[string]*Application
	bySession         map[string]string
	operationMu       map[string]*sync.Mutex
	execCancels       map[string]context.CancelFunc
	execStorageErrors map[string]error
	waiters           []*slotWaiter
	changed           chan struct{}
	starting          chan struct{}
	closed            bool
}

// NewManager restores persisted records into memory. Reconcile must be called
// before serving requests so Docker labels and bbolt state are brought back to
// one authoritative view.
func NewManager(cfg Config, engine Engine, store StateStore, clock Clock) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid sandbox config: %w", err)
	}
	if engine == nil || store == nil {
		return nil, errors.New("sandbox engine and state store are required")
	}
	if clock == nil {
		clock = realClock{}
	}
	records, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("load sandbox state: %w", err)
	}
	manager := &Manager{
		cfg:               cfg,
		engine:            engine,
		store:             store,
		clock:             clock,
		applications:      make(map[string]*Application, len(records)),
		bySession:         make(map[string]string, len(records)),
		operationMu:       make(map[string]*sync.Mutex, len(records)),
		execCancels:       make(map[string]context.CancelFunc),
		execStorageErrors: make(map[string]error),
		changed:           make(chan struct{}),
		starting:          make(chan struct{}, cfg.MaxStarting),
	}
	for i := range records {
		record := records[i]
		if err := validatePersistedApplication(cfg, record); err != nil {
			return nil, fmt.Errorf("invalid persisted sandbox record %q: %w", record.ID, err)
		}
		if _, exists := manager.applications[record.ID]; exists {
			return nil, fmt.Errorf("duplicate persisted application %q", record.ID)
		}
		if existing, exists := manager.bySession[record.SessionID]; exists {
			return nil, fmt.Errorf("session has duplicate applications %q and %q", existing, record.ID)
		}
		copy := cloneApplication(record)
		manager.clampIdleTTL(&copy)
		manager.applications[copy.ID] = &copy
		manager.bySession[copy.SessionID] = copy.ID
		manager.operationMu[copy.ID] = &sync.Mutex{}
	}
	return manager, nil
}

func (manager *Manager) Apply(ctx context.Context, identity Identity) (ApplyResult, error) {
	if err := validateIdentity(identity, true); err != nil {
		return ApplyResult{}, err
	}
	for {
		manager.mu.Lock()
		if manager.closed {
			manager.mu.Unlock()
			return ApplyResult{}, internalError("controller is closed", nil)
		}
		if applicationID := manager.bySession[identity.SessionID]; applicationID != "" {
			application := manager.applications[applicationID]
			if manager.clock.Now().Before(application.HardExpiresAt) && application.State != StateDestroyed && application.State != StateDestroying {
				if pending := manager.pendingApplyOperationLocked(application); pending != nil {
					manager.mu.Unlock()
					if err := manager.lockOperation(ctx, pending); err != nil {
						return ApplyResult{}, canceledError(err)
					}
					manager.unlockOperation(pending)
					continue
				}
				view := viewOf(*application)
				manager.mu.Unlock()
				return ApplyResult{Application: view, Created: false}, nil
			}
			manager.mu.Unlock()
			if err := manager.destroyOwned(ctx, identity.SessionID, applicationID, true); err != nil {
				return ApplyResult{}, err
			}
			continue
		}
		manager.mu.Unlock()

		if err := manager.checkStoragePressure(ctx, ""); err != nil {
			return ApplyResult{}, err
		}

		manager.mu.Lock()
		if manager.closed {
			manager.mu.Unlock()
			return ApplyResult{}, internalError("controller is closed", nil)
		}
		if applicationID := manager.bySession[identity.SessionID]; applicationID != "" {
			application := manager.applications[applicationID]
			if manager.clock.Now().Before(application.HardExpiresAt) && application.State != StateDestroyed && application.State != StateDestroying {
				if pending := manager.pendingApplyOperationLocked(application); pending != nil {
					manager.mu.Unlock()
					if err := manager.lockOperation(ctx, pending); err != nil {
						return ApplyResult{}, canceledError(err)
					}
					manager.unlockOperation(pending)
					continue
				}
				view := viewOf(*application)
				manager.mu.Unlock()
				return ApplyResult{Application: view, Created: false}, nil
			}
			manager.mu.Unlock()
			if err := manager.destroyOwned(ctx, identity.SessionID, applicationID, true); err != nil {
				return ApplyResult{}, err
			}
			continue
		}
		if len(manager.applications) >= manager.cfg.MaxApplications {
			manager.mu.Unlock()
			return ApplyResult{}, &Error{Code: CodeApplicationLimit, Message: "sandbox application limit reached", RetryAfter: manager.cfg.ReaperInterval}
		}
		applicationID, err := newApplicationID()
		if err != nil {
			manager.mu.Unlock()
			return ApplyResult{}, internalError("generate application id", err)
		}
		now := manager.clock.Now().UTC()
		application := &Application{
			ID:                 applicationID,
			SessionID:          identity.SessionID,
			VolumeName:         volumeName(manager.cfg.Namespace, applicationID),
			State:              StateApplied,
			CreatedAt:          now,
			HardExpiresAt:      now.Add(manager.cfg.HardTTL),
			IdleTTL:            manager.cfg.BaseIdleTTL,
			WorkspacePreserved: true,
		}
		operation := &sync.Mutex{}
		operation.Lock()
		application.Revision = 1
		manager.applications[application.ID] = application
		manager.bySession[application.SessionID] = application.ID
		manager.operationMu[application.ID] = operation
		view := viewOf(*application)
		persistedRecord := cloneApplication(*application)
		manager.signalLocked()
		manager.mu.Unlock()

		// The in-memory reservation makes one-application-per-session atomic
		// without holding the global Manager mutex across durable or Docker I/O. Any
		// create/exec that races this provisioning waits on the per-application
		// operation lock and observes either a ready volume or a rolled-back ID.
		if err := manager.store.Put(persistedRecord); err != nil {
			cleanupErr := manager.rollbackFailedApply(application)
			manager.unlockOperation(operation)
			return ApplyResult{}, internalError("persist sandbox state", errors.Join(err, cleanupErr))
		}
		provisionTimeout := applyProvisionTimeout
		remaining := application.HardExpiresAt.Sub(manager.clock.Now())
		hardDeadlineIsLimit := remaining <= provisionTimeout
		if callerDeadline, ok := ctx.Deadline(); ok && !callerDeadline.After(time.Now().Add(remaining)) {
			hardDeadlineIsLimit = false
		}
		if remaining < provisionTimeout {
			provisionTimeout = remaining
		}
		if provisionTimeout <= 0 {
			cleanupErr := manager.rollbackFailedApply(application)
			manager.unlockOperation(operation)
			if cleanupErr != nil {
				return ApplyResult{}, internalError("clean up expired sandbox application", cleanupErr)
			}
			return ApplyResult{}, &Error{Code: CodeExpired, Message: "sandbox application expired during provisioning"}
		}
		provisionContext, provisionCancel := context.WithTimeout(ctx, provisionTimeout)
		_, provisionErr := manager.ensureWorkspaceVolume(provisionContext, application)
		provisionContextErr := provisionContext.Err()
		provisionCancel()
		if provisionErr != nil {
			cleanupErr := manager.rollbackFailedApply(application)
			manager.unlockOperation(operation)
			if cleanupErr != nil {
				return ApplyResult{}, internalError("clean up failed sandbox application", errors.Join(provisionErr, cleanupErr))
			}
			if manager.applicationExpired(application) || hardDeadlineIsLimit && errors.Is(provisionContextErr, context.DeadlineExceeded) {
				return ApplyResult{}, &Error{Code: CodeExpired, Message: "sandbox application expired during provisioning"}
			}
			if ctx.Err() != nil {
				return ApplyResult{}, canceledError(ctx.Err())
			}
			return ApplyResult{}, internalError("create workspace volume", errors.Join(provisionErr, cleanupErr))
		}
		if manager.applicationExpired(application) {
			expiryErr := manager.expireLockedOperation(application)
			manager.unlockOperation(operation)
			return ApplyResult{}, expiryErr
		}
		if ctx.Err() != nil {
			cleanupErr := manager.rollbackFailedApply(application)
			manager.unlockOperation(operation)
			if cleanupErr != nil {
				return ApplyResult{}, internalError("clean up canceled sandbox application", cleanupErr)
			}
			return ApplyResult{}, canceledError(ctx.Err())
		}
		manager.unlockOperation(operation)
		return ApplyResult{Application: view, Created: true}, nil
	}
}

// pendingApplyOperationLocked distinguishes a fully applied workspace from the
// short reservation window in which Apply has published the application ID but
// has not yet confirmed its durable volume. manager.mu must be held.
func (manager *Manager) pendingApplyOperationLocked(application *Application) *sync.Mutex {
	if application.State != StateApplied {
		return nil
	}
	operation := manager.operationMu[application.ID]
	if operation == nil {
		return nil
	}
	if operation.TryLock() {
		operation.Unlock()
		return nil
	}
	return operation
}

// rollbackFailedApply removes only an exactly owned volume. If Docker cleanup
// cannot be confirmed, the durable application remains in Destroying so Reap
// can retry without orphaning a managed resource.
func (manager *Manager) rollbackFailedApply(application *Application) error {
	var cleanupErr error
	cleanupErr = withCleanupTimeout(func(cleanupContext context.Context) error {
		info, err := manager.engine.InspectVolume(cleanupContext, application.VolumeName)
		if err != nil || !info.Exists {
			return err
		}
		if !sameOwnershipLabels(info.Labels, manager.labelsFor(application, "volume")) {
			return nil
		}
		return manager.engine.RemoveVolume(cleanupContext, application.VolumeName)
	})
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.applications[application.ID]
	if current != application {
		return cleanupErr
	}
	if cleanupErr != nil {
		application.State = StateDestroying
		persistErr := manager.persistLocked(application)
		manager.signalLocked()
		return errors.Join(cleanupErr, persistErr)
	}
	if err := manager.store.Delete(application.ID); err != nil {
		manager.signalLocked()
		return internalError("delete failed application state", err)
	}
	application.State = StateDestroyed
	delete(manager.applications, application.ID)
	delete(manager.bySession, application.SessionID)
	delete(manager.operationMu, application.ID)
	manager.signalLocked()
	return nil
}

func (manager *Manager) Create(ctx context.Context, identity Identity, applicationID string) (ApplicationResult, error) {
	if err := validateIdentity(identity, true); err != nil {
		return ApplicationResult{}, err
	}
	operation, err := manager.authorizedOperation(ctx, identity.SessionID, applicationID, false)
	if err != nil {
		return ApplicationResult{}, err
	}
	defer manager.unlockOperation(operation)
	if err := manager.checkStoragePressure(ctx, applicationID); err != nil {
		return ApplicationResult{}, err
	}
	application, err := manager.authorizedApplication(identity.SessionID, applicationID)
	if err != nil {
		return ApplicationResult{}, err
	}
	var queueDuration time.Duration
	if err := manager.ensureActive(ctx, application, &queueDuration); err != nil {
		return ApplicationResult{}, err
	}
	manager.mu.Lock()
	if !manager.clock.Now().Before(application.HardExpiresAt) {
		manager.mu.Unlock()
		return ApplicationResult{}, manager.expireLockedOperation(application)
	}
	manager.touchLocked(application, identity.RunID, StateActive)
	err = manager.persistLocked(application)
	view := viewOf(*application)
	manager.signalLocked()
	manager.mu.Unlock()
	if err != nil {
		return ApplicationResult{}, err
	}
	return ApplicationResult{Application: view, QueueDurationMS: float64(queueDuration) / float64(time.Millisecond)}, nil
}

func (manager *Manager) Status(ctx context.Context, identity Identity, applicationID string) (ApplicationResult, error) {
	if err := validateIdentity(identity, true); err != nil {
		return ApplicationResult{}, err
	}
	manager.mu.Lock()
	application := manager.applications[applicationID]
	if application == nil || application.SessionID != identity.SessionID || application.State == StateDestroyed || application.State == StateDestroying {
		manager.mu.Unlock()
		return ApplicationResult{}, notFoundError()
	}
	hardExpiresAt := application.HardExpiresAt
	manager.mu.Unlock()
	if !manager.clock.Now().Before(hardExpiresAt) {
		_ = manager.destroyOwned(ctx, identity.SessionID, applicationID, true)
		return ApplicationResult{}, &Error{Code: CodeExpired, Message: "sandbox application expired"}
	}
	manager.storageMu.Lock()
	usage, usageErr := manager.engine.StorageUsage(ctx, manager.cfg.Namespace)
	manager.storageMu.Unlock()
	if usageErr != nil {
		return ApplicationResult{}, internalError("measure sandbox storage", usageErr)
	}
	manager.mu.Lock()
	application = manager.applications[applicationID]
	if application == nil || application.SessionID != identity.SessionID || application.State == StateDestroyed || application.State == StateDestroying {
		manager.mu.Unlock()
		return ApplicationResult{}, notFoundError()
	}
	application.WorkspaceBytes = usage.WorkspaceBytes[application.VolumeName]
	view := viewOf(*application)
	manager.mu.Unlock()
	return ApplicationResult{Application: view}, nil
}

func (manager *Manager) Exec(ctx context.Context, identity Identity, applicationID string, request ExecRequest) (ExecResult, error) {
	if err := validateIdentity(identity, true); err != nil {
		return ExecResult{}, err
	}
	command := strings.TrimSpace(request.Command)
	if command == "" || len(command) > maxCommandBytes || strings.ContainsRune(command, '\x00') {
		return ExecResult{}, invalidRequest(fmt.Sprintf("command must contain 1 to %d bytes without NUL", maxCommandBytes))
	}
	workingDirectory, err := validateWorkspacePath(request.CWD, true, true)
	if err != nil {
		return ExecResult{}, invalidRequest("cwd " + err.Error())
	}
	if len(request.Stdin) > maxStdinBytes {
		return ExecResult{}, invalidRequest(fmt.Sprintf("stdin exceeds %d bytes", maxStdinBytes))
	}
	timeout, err := manager.commandTimeout(request.TimeoutSeconds)
	if err != nil {
		return ExecResult{}, err
	}
	operation, err := manager.authorizedOperation(ctx, identity.SessionID, applicationID, true)
	if err != nil {
		return ExecResult{}, err
	}
	defer manager.unlockOperation(operation)
	if err := manager.checkStoragePressure(ctx, applicationID); err != nil {
		return ExecResult{}, err
	}
	application, err := manager.authorizedApplication(identity.SessionID, applicationID)
	if err != nil {
		return ExecResult{}, err
	}
	started := time.Now()
	engineResult, err := manager.runOperation(ctx, application, identity.RunID, timeout, true, true, EngineExecRequest{
		User:        "1000:1000",
		Command:     []string{"/bin/bash", "-c", execCommandScript, "easygo-exec", workingDirectory, command},
		WorkingDir:  "/workspace",
		Stdin:       request.Stdin,
		StdoutLimit: manager.cfg.MaxOutputBytes,
		StderrLimit: manager.cfg.MaxOutputBytes,
	})
	if err != nil {
		return ExecResult{}, err
	}
	manager.mu.Lock()
	view := viewOf(*application)
	manager.mu.Unlock()
	return ExecResult{
		Application: view,
		ExitCode:    engineResult.ExitCode,
		Stdout:      engineResult.Stdout,
		Stderr:      engineResult.Stderr,
		Truncated:   engineResult.StdoutTruncated || engineResult.StderrTruncated,
		DurationMS:  time.Since(started).Milliseconds(),
	}, nil
}

func (manager *Manager) WriteFile(ctx context.Context, identity Identity, applicationID string, request WriteFileRequest) (WriteFileResult, error) {
	if err := validateIdentity(identity, true); err != nil {
		return WriteFileResult{}, err
	}
	filePath, err := validateWorkspacePath(request.Path, false, false)
	if err != nil {
		return WriteFileResult{}, invalidRequest("path " + err.Error())
	}
	if len(request.Content) > maxStdinBytes || !utf8.ValidString(request.Content) {
		return WriteFileResult{}, invalidRequest(fmt.Sprintf("content must be valid UTF-8 and at most %d bytes", maxStdinBytes))
	}
	operation, err := manager.authorizedOperation(ctx, identity.SessionID, applicationID, true)
	if err != nil {
		return WriteFileResult{}, err
	}
	defer manager.unlockOperation(operation)
	if err := manager.checkStoragePressure(ctx, applicationID); err != nil {
		return WriteFileResult{}, err
	}
	application, err := manager.authorizedApplication(identity.SessionID, applicationID)
	if err != nil {
		return WriteFileResult{}, err
	}
	appendFlag, executableFlag := "0", "0"
	if request.Append {
		appendFlag = "1"
	}
	if request.Executable {
		executableFlag = "1"
	}
	result, err := manager.runOperation(ctx, application, identity.RunID, manager.cfg.CommandTimeout, false, false, EngineExecRequest{
		User:        "1000:1000",
		Command:     []string{"/bin/bash", "-c", writeFileScript, "easygo-write", filePath, appendFlag, executableFlag},
		WorkingDir:  "/workspace",
		Stdin:       request.Content,
		StdoutLimit: 64,
		StderrLimit: manager.cfg.MaxOutputBytes,
	})
	if err != nil {
		return WriteFileResult{}, err
	}
	if result.ExitCode != 0 {
		_ = withCleanupTimeout(func(cleanupContext context.Context) error {
			return manager.hibernate(cleanupContext, application)
		})
		return WriteFileResult{}, invalidRequest(fileOperationMessage("write file", result))
	}
	size, err := strconv.ParseInt(strings.TrimSpace(result.Stdout), 10, 64)
	if err != nil || size < 0 {
		_ = withCleanupTimeout(func(cleanupContext context.Context) error {
			return manager.hibernate(cleanupContext, application)
		})
		return WriteFileResult{}, internalError("parse written file size", err)
	}
	if err := manager.renewSuccessfulOperation(application, identity.RunID); err != nil {
		return WriteFileResult{}, err
	}
	manager.mu.Lock()
	view := viewOf(*application)
	manager.mu.Unlock()
	return WriteFileResult{Application: view, Path: filePath, SizeBytes: size}, nil
}

func (manager *Manager) ReadFile(ctx context.Context, identity Identity, applicationID string, request ReadFileRequest) (ReadFileResult, error) {
	if err := validateIdentity(identity, true); err != nil {
		return ReadFileResult{}, err
	}
	filePath, err := validateWorkspacePath(request.Path, false, false)
	if err != nil {
		return ReadFileResult{}, invalidRequest("path " + err.Error())
	}
	if request.Offset < 0 {
		return ReadFileResult{}, invalidRequest("offset cannot be negative")
	}
	if request.MaxBytes == 0 {
		request.MaxBytes = manager.cfg.MaxOutputBytes
	}
	if request.MaxBytes < 1 || request.MaxBytes > manager.cfg.MaxOutputBytes {
		return ReadFileResult{}, invalidRequest(fmt.Sprintf("max_bytes must be between 1 and %d", manager.cfg.MaxOutputBytes))
	}
	operation, err := manager.authorizedOperation(ctx, identity.SessionID, applicationID, true)
	if err != nil {
		return ReadFileResult{}, err
	}
	defer manager.unlockOperation(operation)
	application, err := manager.authorizedApplication(identity.SessionID, applicationID)
	if err != nil {
		return ReadFileResult{}, err
	}
	result, err := manager.runOperation(ctx, application, identity.RunID, manager.cfg.CommandTimeout, false, false, EngineExecRequest{
		User:        "1000:1000",
		Command:     []string{"/bin/bash", "-c", readFileScript, "easygo-read", filePath, strconv.FormatInt(request.Offset, 10), strconv.Itoa(request.MaxBytes)},
		WorkingDir:  "/workspace",
		StdoutLimit: request.MaxBytes + 64,
		StderrLimit: manager.cfg.MaxOutputBytes,
	})
	if err != nil {
		return ReadFileResult{}, err
	}
	if result.ExitCode != 0 {
		_ = withCleanupTimeout(func(cleanupContext context.Context) error {
			return manager.hibernate(cleanupContext, application)
		})
		return ReadFileResult{}, invalidRequest(fileOperationMessage("read file", result))
	}
	header, content, found := strings.Cut(result.Stdout, "\n")
	if !found {
		_ = withCleanupTimeout(func(cleanupContext context.Context) error {
			return manager.hibernate(cleanupContext, application)
		})
		return ReadFileResult{}, internalError("decode read file response", nil)
	}
	size, err := strconv.ParseInt(header, 10, 64)
	if err != nil || size < 0 {
		_ = withCleanupTimeout(func(cleanupContext context.Context) error {
			return manager.hibernate(cleanupContext, application)
		})
		return ReadFileResult{}, internalError("decode read file size", err)
	}
	content, valid := completeUTF8Prefix(content)
	if !valid || content == "" && request.Offset < size {
		_ = withCleanupTimeout(func(cleanupContext context.Context) error {
			return manager.hibernate(cleanupContext, application)
		})
		return ReadFileResult{}, invalidRequest("file is not valid UTF-8 at the requested offset, or max_bytes is too small for the next character")
	}
	nextOffset := request.Offset + int64(len(content))
	if nextOffset > size {
		nextOffset = size
	}
	if err := manager.renewSuccessfulOperation(application, identity.RunID); err != nil {
		return ReadFileResult{}, err
	}
	manager.mu.Lock()
	view := viewOf(*application)
	manager.mu.Unlock()
	return ReadFileResult{Application: view, Path: filePath, Content: content, SizeBytes: size, NextOffset: nextOffset, EOF: nextOffset >= size}, nil
}

func (manager *Manager) Release(ctx context.Context, identity Identity, applicationID string) (ApplicationResult, error) {
	if err := validateIdentity(identity, true); err != nil {
		return ApplicationResult{}, err
	}
	manager.mu.Lock()
	application := manager.applications[applicationID]
	if application == nil || application.SessionID != identity.SessionID || application.State == StateDestroyed || application.State == StateDestroying {
		manager.mu.Unlock()
		return ApplicationResult{}, notFoundError()
	}
	if cancel := manager.execCancels[applicationID]; cancel != nil {
		cancel()
	}
	manager.mu.Unlock()
	operation, err := manager.authorizedOperation(ctx, identity.SessionID, applicationID, false)
	if err != nil {
		return ApplicationResult{}, err
	}
	defer manager.unlockOperation(operation)
	application, err = manager.authorizedApplication(identity.SessionID, applicationID)
	if err != nil {
		return ApplicationResult{}, err
	}
	if err := manager.hibernate(ctx, application); err != nil {
		return ApplicationResult{}, err
	}
	manager.mu.Lock()
	view := viewOf(*application)
	manager.mu.Unlock()
	return ApplicationResult{Application: view}, nil
}

// Destroy is idempotent for an already absent application. An application
// owned by a different session is deliberately indistinguishable from absent.
func (manager *Manager) Destroy(ctx context.Context, identity Identity, applicationID string) error {
	if err := validateIdentity(identity, true); err != nil {
		return err
	}
	return manager.destroyOwned(ctx, identity.SessionID, applicationID, false)
}

func (manager *Manager) runOperation(ctx context.Context, application *Application, runID string, timeout time.Duration, renew, monitorStorage bool, request EngineExecRequest) (EngineExecResult, error) {
	if err := manager.ensureActive(ctx, application, nil); err != nil {
		return EngineExecResult{}, err
	}
	manager.mu.Lock()
	now := manager.clock.Now()
	remaining := application.HardExpiresAt.Sub(now)
	if remaining <= 0 {
		manager.mu.Unlock()
		return EngineExecResult{}, manager.expireLockedOperation(application)
	}
	hardDeadlineIsLimit := remaining <= timeout
	if hardDeadlineIsLimit {
		hardDeadline := time.Now().Add(remaining)
		if requestDeadline, ok := ctx.Deadline(); ok && !requestDeadline.After(hardDeadline) {
			hardDeadlineIsLimit = false
		}
		timeout = remaining
	}
	application.State = StateBusy
	if err := manager.persistLocked(application); err != nil {
		manager.mu.Unlock()
		return EngineExecResult{}, err
	}
	containerID := application.ContainerID
	manager.signalLocked()
	manager.mu.Unlock()

	execContext, cancel := context.WithTimeout(ctx, timeout)
	manager.mu.Lock()
	manager.execCancels[application.ID] = cancel
	delete(manager.execStorageErrors, application.ID)
	manager.mu.Unlock()
	var stopStorageMonitor context.CancelFunc
	var storageMonitorDone chan struct{}
	if monitorStorage {
		storageContext, stop := context.WithCancel(execContext)
		stopStorageMonitor = stop
		storageMonitorDone = make(chan struct{})
		go manager.monitorExecStorage(storageContext, application.ID, storageMonitorDone)
	}
	result, execErr := manager.engine.Exec(execContext, containerID, request)
	execContextErr := execContext.Err()
	if stopStorageMonitor != nil {
		stopStorageMonitor()
		<-storageMonitorDone
	}
	cancel()
	manager.mu.Lock()
	storageErr := manager.execStorageErrors[application.ID]
	delete(manager.execStorageErrors, application.ID)
	delete(manager.execCancels, application.ID)
	manager.mu.Unlock()
	if manager.applicationExpired(application) {
		return EngineExecResult{}, manager.expireLockedOperation(application)
	}

	if execErr == nil && execContextErr == nil && storageErr == nil {
		containerDiscarded := false
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		cleanupResult, cleanupErr := manager.engine.Exec(cleanupContext, containerID, EngineExecRequest{
			User:        "1000:1000",
			Command:     []string{"/bin/sh", "-c", cleanupUserProcessesScript},
			WorkingDir:  "/workspace",
			StdoutLimit: 1,
			StderrLimit: 256,
		})
		if cleanupErr == nil && cleanupResult.ExitCode != 0 {
			cleanupErr = errors.New("uid 1000 processes remain after cleanup")
		}
		cleanupCancel()
		if cleanupErr != nil {
			// A stop/start reset introduces another ambiguous Docker start boundary:
			// the client can return an error before a late daemon-side start takes
			// effect. Removing disposable compute kills every background process and
			// makes resurrection impossible while retaining the workspace volume.
			cleanupErr = manager.discardDisposableContainer(application, containerID, StateHibernated)
			containerDiscarded = cleanupErr == nil
		}
		if cleanupErr == nil {
			manager.mu.Lock()
			recoveredState := StateWarmIdle
			if containerDiscarded {
				recoveredState = StateHibernated
			}
			if renew {
				manager.touchLocked(application, runID, recoveredState)
			} else {
				application.State = recoveredState
			}
			if containerDiscarded {
				application.IdleExpiresAt = time.Time{}
			}
			persistErr := manager.persistLocked(application)
			manager.signalLocked()
			manager.mu.Unlock()
			if persistErr != nil {
				return EngineExecResult{}, persistErr
			}
			return result, nil
		}
		execErr = fmt.Errorf("cleanup background processes: %w", cleanupErr)
	}

	stopErr := withCleanupTimeout(func(cleanupContext context.Context) error {
		return manager.engine.StopContainer(cleanupContext, containerID)
	})
	if (hardDeadlineIsLimit && errors.Is(execContextErr, context.DeadlineExceeded)) || !manager.clock.Now().Before(application.HardExpiresAt) {
		return EngineExecResult{}, manager.expireLockedOperation(application)
	}
	if stopErr != nil {
		// The container may still be running. Keep the durable Busy state so it
		// continues to consume a slot; Reap recognizes a Busy application with no
		// registered exec and retries the stop.
		manager.mu.Lock()
		application.State = StateBusy
		manager.signalLocked()
		manager.mu.Unlock()
		return EngineExecResult{}, internalError("stop sandbox after interrupted command", stopErr)
	}
	manager.mu.Lock()
	application.State = StateHibernated
	application.IdleExpiresAt = time.Time{}
	persistErr := manager.persistLocked(application)
	manager.signalLocked()
	manager.mu.Unlock()
	if persistErr != nil {
		return EngineExecResult{}, persistErr
	}
	if storageErr != nil {
		return EngineExecResult{}, storageErr
	}
	if errors.Is(execContextErr, context.DeadlineExceeded) {
		return EngineExecResult{}, &Error{Code: CodeCommandTimeout, Message: "sandbox command timed out"}
	}
	if errors.Is(execContextErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return EngineExecResult{}, &Error{Code: CodeCanceled, Message: "sandbox request canceled"}
	}
	return EngineExecResult{}, internalError("execute sandbox command", execErr)
}

func (manager *Manager) monitorExecStorage(ctx context.Context, applicationID string, done chan<- struct{}) {
	defer close(done)
	for {
		if err, global := manager.storagePressure(ctx, applicationID); err != nil {
			if ctx.Err() != nil {
				return
			}
			manager.cancelExecsForStorage(applicationID, err, global)
			return
		}
		timer := manager.clock.NewTimer(storageMonitorInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.Channel():
		}
	}
}

func (manager *Manager) cancelExecsForStorage(applicationID string, err error, global bool) {
	manager.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(manager.execCancels))
	for id, cancel := range manager.execCancels {
		if !global && id != applicationID {
			continue
		}
		manager.execStorageErrors[id] = err
		cancels = append(cancels, cancel)
	}
	manager.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (manager *Manager) ensureActive(ctx context.Context, application *Application, queueDuration *time.Duration) error {
	manager.mu.Lock()
	remaining := application.HardExpiresAt.Sub(manager.clock.Now())
	if remaining <= 0 {
		manager.mu.Unlock()
		return manager.expireLockedOperation(application)
	}
	containerID := application.ContainerID
	state := application.State
	manager.mu.Unlock()
	activationDeadline := time.Now().Add(remaining)
	hardDeadlineIsLimit := true
	if requestDeadline, ok := ctx.Deadline(); ok && !requestDeadline.After(activationDeadline) {
		hardDeadlineIsLimit = false
	}
	activationContext, activationCancel := context.WithTimeout(ctx, remaining)
	defer activationCancel()
	activationFailure := func(err error) error {
		if manager.applicationExpired(application) || hardDeadlineIsLimit && errors.Is(activationContext.Err(), context.DeadlineExceeded) {
			return manager.expireLockedOperation(application)
		}
		if activationContext.Err() != nil {
			return canceledError(activationContext.Err())
		}
		return err
	}
	if state == StateStarting {
		// Starting is deliberately conservative: a previous Docker start may have
		// taken effect after its client returned an error. The container is
		// disposable, so remove it before releasing the logical running slot. A
		// later start request for the removed Docker object cannot resurrect it.
		if err := manager.discardDisposableContainer(application, containerID, StateHibernated); err != nil {
			return activationFailure(internalError("discard interrupted sandbox start", err))
		}
		containerID = ""
		state = StateHibernated
	}
	if state == StateHibernating || state == StateBusy {
		// These durable states mean an earlier operation ended before it could
		// confirm a clean stop. This application already occupies a running slot,
		// so re-admitting it would wait on itself. Inspect ownership, stop any
		// retained container to kill stray exec processes, then resume through the
		// normal hibernated activation path.
		if containerID != "" {
			info, err := manager.engine.InspectContainer(activationContext, containerID)
			if err != nil {
				return activationFailure(internalError("inspect interrupted sandbox container", err))
			}
			if info.Exists && manager.expectedContainer(application, info) {
				if info.Running {
					if err := manager.engine.StopContainer(activationContext, containerID); err != nil {
						return activationFailure(internalError("stop interrupted sandbox container", err))
					}
				}
			} else {
				containerID = ""
			}
		}
		manager.mu.Lock()
		application.ContainerID = containerID
		application.State = StateHibernated
		application.IdleExpiresAt = time.Time{}
		err := manager.persistLocked(application)
		manager.signalLocked()
		manager.mu.Unlock()
		if err != nil {
			return err
		}
		state = StateHibernated
	}
	if containerID != "" && (state == StateActive || state == StateWarmIdle) {
		info, err := manager.engine.InspectContainer(activationContext, containerID)
		if err != nil {
			return activationFailure(internalError("inspect sandbox container", err))
		}
		if manager.expectedContainer(application, info) && info.Running {
			if manager.applicationExpired(application) {
				return manager.expireLockedOperation(application)
			}
			return nil
		}
		manager.mu.Lock()
		if !manager.expectedContainer(application, info) {
			application.ContainerID = ""
		}
		application.State = StateHibernated
		_ = manager.persistLocked(application)
		manager.signalLocked()
		manager.mu.Unlock()
	}

	queueStarted := time.Now()
	previousState, err := manager.acquireSlot(activationContext, application.ID)
	if queueDuration != nil {
		*queueDuration = time.Since(queueStarted)
	}
	if err != nil {
		return activationFailure(err)
	}
	if manager.applicationExpired(application) {
		return manager.expireLockedOperation(application)
	}
	select {
	case manager.starting <- struct{}{}:
		defer func() { <-manager.starting }()
	case <-activationContext.Done():
		manager.rollbackStarting(application, previousState)
		return activationFailure(canceledError(activationContext.Err()))
	}

	createdVolume, err := manager.ensureWorkspaceVolume(activationContext, application)
	if err != nil {
		manager.rollbackStarting(application, previousState)
		return activationFailure(internalError("ensure workspace volume", err))
	}
	if createdVolume {
		manager.mu.Lock()
		application.WorkspacePreserved = false
		persistErr := manager.persistLocked(application)
		manager.mu.Unlock()
		if persistErr != nil {
			manager.rollbackStarting(application, previousState)
			return persistErr
		}
	}
	if manager.applicationExpired(application) {
		return manager.expireLockedOperation(application)
	}
	manager.mu.Lock()
	containerID = application.ContainerID
	manager.mu.Unlock()
	containerExists, containerRunning := false, false
	if containerID != "" {
		info, inspectErr := manager.engine.InspectContainer(activationContext, containerID)
		if inspectErr != nil {
			manager.rollbackStarting(application, previousState)
			return activationFailure(internalError("inspect sandbox container", inspectErr))
		}
		containerExists = manager.expectedContainer(application, info)
		containerRunning = containerExists && info.Running
		if !containerExists {
			// A corrupted/stale persisted reference must never authorize deleting
			// an unrelated host container. Drop only our local reference and look
			// up the deterministic managed name below.
			manager.mu.Lock()
			if application.ContainerID == containerID {
				application.ContainerID = ""
				_ = manager.persistLocked(application)
			}
			manager.signalLocked()
			manager.mu.Unlock()
			containerID = ""
		}
	}
	if !containerExists {
		info, inspectErr := manager.engine.InspectContainer(activationContext, containerName(manager.cfg.Namespace, application.ID))
		if inspectErr != nil {
			manager.rollbackStarting(application, previousState)
			return activationFailure(internalError("inspect sandbox container by deterministic name", inspectErr))
		}
		if info.Exists && !manager.expectedContainer(application, info) {
			manager.rollbackStarting(application, previousState)
			return activationFailure(internalError("sandbox container name is owned by an unexpected resource", nil))
		}
		if info.Exists {
			containerID = info.ID
			containerExists = true
			containerRunning = info.Running
			if err := manager.rememberContainer(application, containerID); err != nil {
				return err
			}
		}
	}
	if !containerExists {
		newContainerID, createErr := manager.engine.CreateContainer(activationContext, manager.containerSpec(application))
		if createErr != nil {
			info, recoverErr := manager.recoverContainerAfterAmbiguousCreate(application, newContainerID)
			if recoverErr != nil {
				manager.rollbackStarting(application, previousState)
				return activationFailure(internalError("recover ambiguous sandbox container creation", errors.Join(createErr, recoverErr)))
			}
			if info.Exists {
				containerID = info.ID
				if info.Running {
					if stopErr := manager.stopAfterActivationFailure(application, previousState, containerID); stopErr != nil {
						return stopErr
					}
				} else {
					manager.rollbackStarting(application, previousState)
				}
			} else {
				manager.rollbackStarting(application, previousState)
			}
			return activationFailure(internalError("create sandbox container", createErr))
		}
		containerID = newContainerID
		if err := manager.rememberContainer(application, containerID); err != nil {
			// The deterministic name and ownership labels make this resource
			// recoverable even when the state write failed. Keep StateStarting so
			// it retains a capacity slot until Reap stops it.
			manager.holdStarting(application, containerID)
			return err
		}
	}
	if manager.applicationExpired(application) {
		return manager.expireLockedOperation(application)
	}
	if !containerRunning {
		if err := manager.engine.StartContainer(activationContext, containerID); err != nil {
			// A failed start response is ambiguous: the daemon may still complete
			// the request after this call returns. Force-removing the disposable
			// container is the only recovery that prevents a delayed start from
			// escaping the logical capacity limit. The named workspace volume is
			// intentionally preserved.
			if discardErr := manager.discardDisposableContainer(application, containerID, previousState); discardErr != nil {
				return activationFailure(internalError("recover ambiguous sandbox start", errors.Join(err, discardErr)))
			}
			return activationFailure(internalError("start sandbox container", err))
		}
	}
	if manager.applicationExpired(application) {
		return manager.expireLockedOperation(application)
	}
	if err := manager.waitUntilReady(activationContext, containerID); err != nil {
		if stopErr := manager.stopAfterActivationFailure(application, previousState, containerID); stopErr != nil {
			return stopErr
		}
		return activationFailure(err)
	}
	if manager.applicationExpired(application) {
		return manager.expireLockedOperation(application)
	}
	manager.mu.Lock()
	application.State = StateActive
	err = manager.persistLocked(application)
	if err != nil {
		// The container is confirmed running, while the durable record is still
		// Starting. Preserve that conservative state in memory as well so Reap
		// will stop it and capacity is never released speculatively.
		application.State = StateStarting
	}
	manager.signalLocked()
	manager.mu.Unlock()
	return err
}

func (manager *Manager) ensureWorkspaceVolume(ctx context.Context, application *Application) (bool, error) {
	labels := manager.labelsFor(application, "volume")
	created, err := manager.engine.CreateVolume(ctx, application.VolumeName, labels)
	if err == nil {
		return created, nil
	}
	var info VolumeInfo
	recoverErr := withCleanupTimeout(func(recoveryContext context.Context) error {
		var inspectErr error
		info, inspectErr = manager.engine.InspectVolume(recoveryContext, application.VolumeName)
		return inspectErr
	})
	if recoverErr != nil {
		return false, errors.Join(err, recoverErr)
	}
	if !info.Exists {
		return false, err
	}
	if !sameOwnershipLabels(info.Labels, labels) {
		return false, errors.Join(err, errors.New("workspace volume has unexpected ownership labels"))
	}
	// The create response was ambiguous, but the deterministic, exactly-owned
	// volume exists. Treat it as created; a canceled caller may not observe the
	// result, but a retry will find the persisted application.
	return true, nil
}

func (manager *Manager) expectedContainer(application *Application, info ContainerInfo) bool {
	if !info.Exists || !sameOwnershipLabels(info.Labels, manager.labelsFor(application, "container")) {
		return false
	}
	return info.Name == "" || info.Name == containerName(manager.cfg.Namespace, application.ID)
}

func (manager *Manager) rememberContainer(application *Application, containerID string) error {
	if containerID == "" {
		return internalError("Docker returned an empty sandbox container id", nil)
	}
	manager.mu.Lock()
	application.ContainerID = containerID
	err := manager.persistLocked(application)
	manager.signalLocked()
	manager.mu.Unlock()
	return err
}

func (manager *Manager) recoverContainerAfterAmbiguousCreate(application *Application, hintedID string) (ContainerInfo, error) {
	var recovered ContainerInfo
	err := withCleanupTimeout(func(recoveryContext context.Context) error {
		var inspectErr error
		reference := hintedID
		if reference == "" {
			reference = containerName(manager.cfg.Namespace, application.ID)
		}
		recovered, inspectErr = manager.engine.InspectContainer(recoveryContext, reference)
		if inspectErr != nil {
			return inspectErr
		}
		if !recovered.Exists && hintedID != "" {
			recovered, inspectErr = manager.engine.InspectContainer(recoveryContext, containerName(manager.cfg.Namespace, application.ID))
		}
		return inspectErr
	})
	if err != nil || !recovered.Exists {
		return recovered, err
	}
	if !manager.expectedContainer(application, recovered) {
		return ContainerInfo{}, errors.New("deterministically named container has unexpected ownership labels")
	}
	if err := manager.rememberContainer(application, recovered.ID); err != nil {
		return recovered, err
	}
	return recovered, nil
}

func (manager *Manager) holdStarting(application *Application, containerID string) error {
	manager.mu.Lock()
	application.ContainerID = containerID
	application.State = StateStarting
	err := manager.persistLocked(application)
	manager.signalLocked()
	manager.mu.Unlock()
	return err
}

// discardDisposableContainer removes compute after an interrupted/ambiguous
// Docker start or failed background-process cleanup while preserving the named
// workspace volume. The application remains Starting when removal cannot be
// confirmed, so it keeps a capacity slot and Reap can retry without allowing
// another container to run.
func (manager *Manager) discardDisposableContainer(application *Application, containerID, recoveredState string) error {
	cleanupErr := withCleanupTimeout(func(cleanupContext context.Context) error {
		if containerID == "" {
			return nil
		}
		info, err := manager.engine.InspectContainer(cleanupContext, containerID)
		if err != nil {
			return err
		}
		if !info.Exists {
			return nil
		}
		if !manager.expectedContainer(application, info) {
			return errors.New("sandbox container ownership changed during interrupted start cleanup")
		}
		return manager.engine.RemoveContainer(cleanupContext, containerID)
	})
	if cleanupErr != nil {
		persistErr := manager.holdStarting(application, containerID)
		return errors.Join(cleanupErr, persistErr)
	}

	manager.mu.Lock()
	if application.ContainerID == containerID {
		application.ContainerID = ""
	}
	application.State = recoveredState
	application.IdleExpiresAt = time.Time{}
	persistErr := manager.persistLocked(application)
	if persistErr != nil {
		// The persisted record still describes an unresolved start. Keep the
		// in-memory state conservative too; a subsequent Reap will retry the
		// now-idempotent cleanup and durable transition.
		application.State = StateStarting
	}
	manager.signalLocked()
	manager.mu.Unlock()
	return persistErr
}

func (manager *Manager) stopAfterActivationFailure(application *Application, previousState, containerID string) error {
	stopErr := withCleanupTimeout(func(cleanupContext context.Context) error {
		return manager.engine.StopContainer(cleanupContext, containerID)
	})
	if stopErr != nil {
		persistErr := manager.holdStarting(application, containerID)
		return internalError("stop sandbox after failed activation", errors.Join(stopErr, persistErr))
	}
	manager.rollbackStarting(application, previousState)
	return nil
}

func (manager *Manager) waitUntilReady(ctx context.Context, containerID string) error {
	readyContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		result, err := manager.engine.Exec(readyContext, containerID, EngineExecRequest{
			User:        "1000:1000",
			Command:     []string{"/bin/sh", "-c", readyProbeScript},
			WorkingDir:  "/workspace",
			StdoutLimit: 1,
			StderrLimit: 256,
		})
		if err == nil && result.ExitCode == 0 {
			return nil
		}
		select {
		case <-readyContext.Done():
			if ctx.Err() != nil {
				return canceledError(ctx.Err())
			}
			return internalError("sandbox container did not become ready", readyContext.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (manager *Manager) acquireSlot(ctx context.Context, applicationID string) (string, error) {
	waiter := &slotWaiter{applicationID: applicationID}
	manager.mu.Lock()
	manager.waiters = append(manager.waiters, waiter)
	manager.signalLocked()
	deadline := manager.clock.Now().Add(manager.cfg.WaitTimeout)
	if application := manager.applications[applicationID]; application != nil && application.HardExpiresAt.Before(deadline) {
		deadline = application.HardExpiresAt
	}
	manager.mu.Unlock()

	for {
		manager.mu.Lock()
		application := manager.applications[applicationID]
		if application == nil || application.State == StateDestroyed || application.State == StateDestroying {
			manager.removeWaiterLocked(waiter)
			manager.mu.Unlock()
			return "", notFoundError()
		}
		if manager.clock.Now().Before(application.HardExpiresAt) == false {
			manager.removeWaiterLocked(waiter)
			manager.mu.Unlock()
			return "", &Error{Code: CodeExpired, Message: "sandbox application expired"}
		}
		if len(manager.waiters) > 0 && manager.waiters[0] == waiter {
			previousState := application.State
			running := manager.runningCountLocked()
			var victim *Application
			var victimOperation *sync.Mutex
			var victimState string
			if running >= manager.cfg.MaxRunning {
				victim, victimOperation = manager.oldestIdleLocked(applicationID)
			}
			if running < manager.cfg.MaxRunning || victim != nil {
				if victim != nil {
					victimState = victim.State
					victim.State = StateHibernating
				}
				application.State = StateStarting
				manager.waiters = manager.waiters[1:]
				persistErr := manager.persistLocked(application)
				if persistErr == nil && victim != nil {
					persistErr = manager.persistLocked(victim)
				}
				manager.signalLocked()
				manager.mu.Unlock()
				if persistErr != nil {
					if victim != nil {
						manager.mu.Lock()
						victim.State = victimState
						_ = manager.persistLocked(victim)
						manager.signalLocked()
						manager.mu.Unlock()
					}
					if victimOperation != nil {
						victimOperation.Unlock()
					}
					manager.rollbackStarting(application, previousState)
					return "", persistErr
				}
				if victim != nil {
					if err := manager.engine.StopContainer(ctx, victim.ContainerID); err != nil {
						manager.mu.Lock()
						// Stop was not confirmed. Retain Hibernating so this
						// container still consumes a slot and Reap retries it.
						victim.State = StateHibernating
						_ = manager.persistLocked(victim)
						manager.signalLocked()
						manager.mu.Unlock()
						manager.unlockOperation(victimOperation)
						manager.rollbackStarting(application, previousState)
						return "", internalError("hibernate least-recently-used sandbox", err)
					}
					manager.mu.Lock()
					victim.State = StateHibernated
					victim.IdleExpiresAt = time.Time{}
					_ = manager.persistLocked(victim)
					manager.signalLocked()
					manager.mu.Unlock()
					victimOperation.Unlock()
				}
				return previousState, nil
			}
		}
		remaining := deadline.Sub(manager.clock.Now())
		if remaining <= 0 {
			manager.removeWaiterLocked(waiter)
			manager.mu.Unlock()
			return "", &Error{Code: CodeCapacityExhausted, Message: "all sandbox capacity is busy", RetryAfter: manager.cfg.ReaperInterval}
		}
		changed := manager.changed
		manager.mu.Unlock()
		timer := manager.clock.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			manager.mu.Lock()
			manager.removeWaiterLocked(waiter)
			manager.mu.Unlock()
			return "", canceledError(ctx.Err())
		case <-timer.Channel():
			manager.mu.Lock()
			application := manager.applications[applicationID]
			expired := application != nil && !manager.clock.Now().Before(application.HardExpiresAt)
			manager.removeWaiterLocked(waiter)
			manager.mu.Unlock()
			if expired {
				return "", &Error{Code: CodeExpired, Message: "sandbox application expired"}
			}
			return "", &Error{Code: CodeCapacityExhausted, Message: "all sandbox capacity is busy", RetryAfter: manager.cfg.ReaperInterval}
		case <-changed:
			timer.Stop()
		}
	}
}

func (manager *Manager) hibernate(ctx context.Context, application *Application) error {
	manager.mu.Lock()
	containerID := application.ContainerID
	state := application.State
	manager.mu.Unlock()
	if state == StateStarting {
		return manager.discardDisposableContainer(application, containerID, StateHibernated)
	}
	manager.mu.Lock()
	if containerID != "" && isRunningState(state) {
		application.State = StateHibernating
		if err := manager.persistLocked(application); err != nil {
			manager.signalLocked()
			manager.mu.Unlock()
			return err
		}
		manager.signalLocked()
	}
	manager.mu.Unlock()
	if containerID != "" && isRunningState(state) {
		if err := manager.engine.StopContainer(ctx, containerID); err != nil {
			// Hibernating is intentionally durable: a failed stop may leave a
			// running container, so capacity cannot be released and Reap retries.
			return internalError("stop sandbox container", err)
		}
	}
	manager.mu.Lock()
	application.State = StateHibernated
	application.IdleExpiresAt = time.Time{}
	err := manager.persistLocked(application)
	manager.signalLocked()
	manager.mu.Unlock()
	return err
}

func (manager *Manager) destroyOwned(ctx context.Context, sessionID, applicationID string, expiring bool) error {
	manager.mu.Lock()
	application := manager.applications[applicationID]
	if application == nil {
		manager.mu.Unlock()
		return nil
	}
	if application.SessionID != sessionID {
		manager.mu.Unlock()
		return nil
	}
	if cancel := manager.execCancels[applicationID]; cancel != nil {
		cancel()
	}
	operation := manager.operationMu[applicationID]
	manager.mu.Unlock()
	if err := manager.lockOperation(ctx, operation); err != nil {
		return canceledError(err)
	}
	defer manager.unlockOperation(operation)
	manager.mu.Lock()
	application = manager.applications[applicationID]
	if application == nil {
		manager.mu.Unlock()
		return nil
	}
	if application.SessionID != sessionID {
		manager.mu.Unlock()
		return nil
	}
	manager.mu.Unlock()
	if err := manager.destroyWhileLockedOperation(ctx, application); err != nil {
		return err
	}
	_ = expiring
	return nil
}

func (manager *Manager) destroyWhileLockedOperation(ctx context.Context, application *Application) error {
	manager.mu.Lock()
	if application.State != StateDestroying {
		application.State = StateDestroying
		application.IdleExpiresAt = time.Time{}
		if err := manager.persistLocked(application); err != nil {
			manager.signalLocked()
			manager.mu.Unlock()
			return err
		}
	}
	containerID, volume := application.ContainerID, application.VolumeName
	manager.signalLocked()
	manager.mu.Unlock()
	if containerID != "" {
		info, err := manager.engine.InspectContainer(ctx, containerID)
		if err != nil {
			return internalError("inspect container before removal", err)
		}
		if info.Exists && manager.containerBelongsTo(application, info.Labels) {
			if err := manager.engine.RemoveContainer(ctx, containerID); err != nil {
				return internalError("remove container", err)
			}
		}
		manager.mu.Lock()
		if application.ContainerID == containerID {
			application.ContainerID = ""
		}
		persistErr := manager.persistLocked(application)
		manager.signalLocked()
		manager.mu.Unlock()
		if persistErr != nil {
			return persistErr
		}
	}
	volumeInfo, err := manager.engine.InspectVolume(ctx, volume)
	if err != nil {
		return internalError("inspect volume before removal", err)
	}
	if volumeInfo.Exists {
		if !sameOwnershipLabels(volumeInfo.Labels, manager.labelsFor(application, "volume")) {
			return internalError("workspace volume ownership changed; refusing removal", nil)
		}
		if err := manager.engine.RemoveVolume(ctx, volume); err != nil {
			return internalError("remove volume", err)
		}
	}
	manager.mu.Lock()
	if err := manager.store.Delete(application.ID); err != nil {
		manager.mu.Unlock()
		return internalError("delete sandbox state", err)
	}
	application.State = StateDestroyed
	delete(manager.applications, application.ID)
	delete(manager.bySession, application.SessionID)
	delete(manager.operationMu, application.ID)
	delete(manager.execCancels, application.ID)
	manager.signalLocked()
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) rollbackStarting(application *Application, state string) {
	manager.mu.Lock()
	if application.State == StateStarting {
		application.State = state
		_ = manager.persistLocked(application)
		manager.signalLocked()
	}
	manager.mu.Unlock()
}

func (manager *Manager) authorizedOperation(ctx context.Context, sessionID, applicationID string, rejectBusy bool) (*sync.Mutex, error) {
	for {
		manager.mu.Lock()
		application := manager.applications[applicationID]
		if application == nil || application.SessionID != sessionID || application.State == StateDestroyed || application.State == StateDestroying {
			manager.mu.Unlock()
			return nil, notFoundError()
		}
		operation := manager.operationMu[applicationID]
		state := application.State
		changed := manager.changed
		if operation.TryLock() {
			manager.mu.Unlock()
			return operation, nil
		}
		manager.mu.Unlock()

		// A model-initiated command already executing against this workspace is
		// genuinely concurrent and must be rejected. Controller-owned starting
		// and hibernation transitions are transparent: wait for them, then retry
		// against the resulting state instead of leaking application_busy.
		if rejectBusy && state == StateBusy {
			return nil, &Error{Code: CodeApplicationBusy, Message: "sandbox application is busy"}
		}
		select {
		case <-ctx.Done():
			return nil, canceledError(ctx.Err())
		case <-changed:
		}
	}
}

func (manager *Manager) authorizedApplication(sessionID, applicationID string) (*Application, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	application := manager.applications[applicationID]
	if application == nil || application.SessionID != sessionID || application.State == StateDestroyed || application.State == StateDestroying {
		return nil, notFoundError()
	}
	return application, nil
}

func (manager *Manager) checkStoragePressure(ctx context.Context, applicationID string) error {
	err, _ := manager.storagePressure(ctx, applicationID)
	return err
}

func (manager *Manager) storagePressure(ctx context.Context, applicationID string) (error, bool) {
	manager.storageMu.Lock()
	defer manager.storageMu.Unlock()
	usage, err := manager.engine.StorageUsage(ctx, manager.cfg.Namespace)
	if err != nil {
		return internalError("measure sandbox storage", err), true
	}
	if usage.TotalBytes >= manager.cfg.TotalWorkspaceLimit || usage.FreeBytes >= 0 && usage.FreeBytes < manager.cfg.MinFreeDisk {
		return &Error{Code: CodeStoragePressure, Message: "sandbox total or host free-space safety threshold reached", RetryAfter: manager.cfg.ReaperInterval}, true
	}
	if applicationID != "" {
		manager.mu.Lock()
		application := manager.applications[applicationID]
		manager.mu.Unlock()
		if application != nil && usage.WorkspaceBytes[application.VolumeName] >= manager.cfg.WorkspaceSoftLimit {
			return &Error{Code: CodeStoragePressure, Message: "sandbox workspace soft limit reached", RetryAfter: manager.cfg.ReaperInterval}, false
		}
	}
	return nil, false
}

func (manager *Manager) commandTimeout(seconds int) (time.Duration, error) {
	if seconds < 0 {
		return 0, invalidRequest("timeout_seconds cannot be negative")
	}
	if seconds == 0 {
		return manager.cfg.CommandTimeout, nil
	}
	if int64(seconds) > int64(manager.cfg.MaxCommandTimeout/time.Second) {
		return 0, invalidRequest(fmt.Sprintf("timeout_seconds cannot exceed %d", manager.cfg.MaxCommandTimeout/time.Second))
	}
	return time.Duration(seconds) * time.Second, nil
}

func (manager *Manager) touchLocked(application *Application, runID, state string) {
	now := manager.clock.Now().UTC()
	manager.clampIdleTTL(application)
	seen := false
	for _, existing := range application.SeenRunIDs {
		if existing == runID {
			seen = true
			break
		}
	}
	if !seen {
		if len(application.SeenRunIDs) > 0 {
			idleCap := manager.idleTTLCap(application)
			if application.IdleTTL >= idleCap || application.IdleTTL > idleCap/2 {
				application.IdleTTL = idleCap
			} else {
				application.IdleTTL *= 2
			}
		}
		if application.IdleTTL == 0 {
			application.IdleTTL = manager.cfg.BaseIdleTTL
		}
		application.SeenRunIDs = append(application.SeenRunIDs, runID)
	}
	application.State = state
	application.LastUsedAt = now
	application.IdleExpiresAt = now.Add(application.IdleTTL)
	if application.IdleExpiresAt.After(application.HardExpiresAt) {
		application.IdleExpiresAt = application.HardExpiresAt
	}
}

func (manager *Manager) persistLocked(application *Application) error {
	manager.clampIdleTTL(application)
	application.Revision++
	if err := manager.store.Put(cloneApplication(*application)); err != nil {
		application.Revision--
		return internalError("persist sandbox state", err)
	}
	return nil
}

func (manager *Manager) signalLocked() {
	if manager.closed {
		return
	}
	close(manager.changed)
	manager.changed = make(chan struct{})
}

func (manager *Manager) removeWaiterLocked(target *slotWaiter) {
	for index, waiter := range manager.waiters {
		if waiter == target {
			manager.waiters = append(manager.waiters[:index], manager.waiters[index+1:]...)
			manager.signalLocked()
			return
		}
	}
}

func (manager *Manager) runningCountLocked() int {
	count := 0
	for _, application := range manager.applications {
		if isRunningState(application.State) || application.State == StateDestroying && application.ContainerID != "" {
			count++
		}
	}
	return count
}

func (manager *Manager) oldestIdleLocked(exclude string) (*Application, *sync.Mutex) {
	var candidates []*Application
	for _, application := range manager.applications {
		if application.ID == exclude || application.State != StateActive && application.State != StateWarmIdle {
			continue
		}
		candidates = append(candidates, application)
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i].LastUsedAt, candidates[j].LastUsedAt
		if left.IsZero() {
			left = candidates[i].CreatedAt
		}
		if right.IsZero() {
			right = candidates[j].CreatedAt
		}
		if left.Equal(right) {
			return candidates[i].ID < candidates[j].ID
		}
		return left.Before(right)
	})
	if len(candidates) == 0 {
		return nil, nil
	}
	for _, candidate := range candidates {
		operation := manager.operationMu[candidate.ID]
		if operation != nil && operation.TryLock() {
			return candidate, operation
		}
	}
	return nil, nil
}

func (manager *Manager) renewSuccessfulOperation(application *Application, runID string) error {
	manager.mu.Lock()
	state := StateWarmIdle
	if application.ContainerID == "" {
		state = StateHibernated
	}
	manager.touchLocked(application, runID, state)
	if state == StateHibernated {
		application.IdleExpiresAt = time.Time{}
	}
	err := manager.persistLocked(application)
	manager.signalLocked()
	manager.mu.Unlock()
	return err
}

func (manager *Manager) unlockOperation(operation *sync.Mutex) {
	operation.Unlock()
	manager.mu.Lock()
	manager.signalLocked()
	manager.mu.Unlock()
}

func (manager *Manager) applicationExpired(application *Application) bool {
	return !manager.clock.Now().Before(application.HardExpiresAt)
}

func (manager *Manager) idleTTLCap(application *Application) time.Duration {
	cap := manager.cfg.HardTTL
	lifetime := application.HardExpiresAt.Sub(application.CreatedAt)
	if lifetime > 0 && lifetime < cap {
		cap = lifetime
	}
	if cap > maximumHardTTL {
		cap = maximumHardTTL
	}
	return cap
}

func (manager *Manager) clampIdleTTL(application *Application) {
	if cap := manager.idleTTLCap(application); application.IdleTTL > cap {
		application.IdleTTL = cap
	}
}

// expireLockedOperation is called only while the application's operation lock
// is held. Cleanup gets its own bounded context because the request or hard
// deadline context that led here is normally already canceled.
func (manager *Manager) expireLockedOperation(application *Application) error {
	if err := withCleanupTimeout(func(cleanupContext context.Context) error {
		return manager.destroyWhileLockedOperation(cleanupContext, application)
	}); err != nil {
		return err
	}
	return &Error{Code: CodeExpired, Message: "sandbox application expired"}
}

func withCleanupTimeout(operation func(context.Context) error) error {
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), cleanupOperationTimeout)
	defer cleanupCancel()
	return operation(cleanupContext)
}

func (manager *Manager) labelsFor(application *Application, resource string) map[string]string {
	return map[string]string{
		managedLabel:     "true",
		namespaceLabel:   manager.cfg.Namespace,
		applicationLabel: application.ID,
		resourceLabel:    resource,
		hardExpiryLabel:  application.HardExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}

func (manager *Manager) containerSpec(application *Application) ContainerSpec {
	return ContainerSpec{
		Name:       containerName(manager.cfg.Namespace, application.ID),
		Image:      manager.cfg.Image,
		VolumeName: application.VolumeName,
		Labels:     manager.labelsFor(application, "container"),
		CPUs:       manager.cfg.CPUs,
		Memory:     manager.cfg.MemoryBytes,
		PIDsLimit:  manager.cfg.PIDsLimit,
		ShmSize:    manager.cfg.ShmSizeBytes,
		TmpSize:    manager.cfg.TmpSizeBytes,
	}
}

func (manager *Manager) containerBelongsTo(application *Application, labels map[string]string) bool {
	return sameOwnershipLabels(labels, manager.labelsFor(application, "container"))
}

func isRunningState(state string) bool {
	switch state {
	case StateStarting, StateActive, StateBusy, StateWarmIdle, StateHibernating:
		return true
	default:
		return false
	}
}

func validateIdentity(identity Identity, requireRun bool) error {
	if !validIdentityPart(identity.SessionID) {
		return invalidRequest("trusted session identity is missing or invalid")
	}
	if requireRun && !validIdentityPart(identity.RunID) {
		return invalidRequest("trusted run identity is missing or invalid")
	}
	return nil
}

func validatePersistedApplication(cfg Config, application Application) error {
	if !persistedApplicationIDPattern.MatchString(application.ID) {
		return errors.New("application id is invalid")
	}
	if !validIdentityPart(application.SessionID) {
		return errors.New("session id is invalid")
	}
	if application.VolumeName != volumeName(cfg.Namespace, application.ID) {
		return errors.New("workspace volume name does not match namespace and application id")
	}
	if application.ContainerID != "" && !dockerContainerIDPattern.MatchString(application.ContainerID) {
		return errors.New("container id is invalid")
	}
	switch application.State {
	case StateApplied, StateStarting, StateActive, StateBusy, StateWarmIdle, StateHibernating, StateHibernated, StateDestroying, StateDestroyed:
	default:
		return errors.New("application state is invalid")
	}
	lifetime := application.HardExpiresAt.Sub(application.CreatedAt)
	if application.CreatedAt.IsZero() || application.HardExpiresAt.IsZero() || lifetime <= 0 || lifetime > maximumHardTTL {
		return errors.New("hard deadline is invalid")
	}
	if application.IdleTTL <= 0 || application.IdleTTL > maximumHardTTL || application.IdleTTL > lifetime {
		return errors.New("idle ttl is invalid")
	}
	if !application.LastUsedAt.IsZero() && (application.LastUsedAt.Before(application.CreatedAt) || application.LastUsedAt.After(application.HardExpiresAt)) {
		return errors.New("last-used time is outside the application lifetime")
	}
	if !application.IdleExpiresAt.IsZero() && (application.IdleExpiresAt.Before(application.CreatedAt) || application.IdleExpiresAt.After(application.HardExpiresAt)) {
		return errors.New("idle deadline is outside the application lifetime")
	}
	seen := make(map[string]struct{}, len(application.SeenRunIDs))
	for _, runID := range application.SeenRunIDs {
		if !validIdentityPart(runID) {
			return errors.New("seen run id is invalid")
		}
		if _, duplicate := seen[runID]; duplicate {
			return errors.New("seen run ids contain a duplicate")
		}
		seen[runID] = struct{}{}
	}
	return nil
}

func validIdentityPart(value string) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool { return r < 33 || r == 127 }) < 0
}

func validateWorkspacePath(value string, optional, allowRoot bool) (string, error) {
	if value == "" && optional {
		return "/workspace", nil
	}
	if value == "" || len(value) > maxPathBytes || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("must contain 1 to %d bytes without NUL", maxPathBytes)
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned != "/workspace" && !strings.HasPrefix(cleaned, "/workspace/") {
		return "", errors.New("must be a canonical absolute path under /workspace")
	}
	if !allowRoot && cleaned == "/workspace" {
		return "", errors.New("must name a file below /workspace")
	}
	return cleaned, nil
}

func newApplicationID() (string, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "app_" + base64.RawURLEncoding.EncodeToString(random), nil
}

func volumeName(namespace, applicationID string) string {
	return namespace + "-workspace-" + strings.ToLower(applicationID)
}

func containerName(namespace, applicationID string) string {
	return namespace + "-sandbox-" + strings.ToLower(applicationID)
}

func viewOf(application Application) ApplicationView {
	view := ApplicationView{
		ID:                 application.ID,
		State:              application.State,
		CreatedAt:          application.CreatedAt.UTC().Format(time.RFC3339Nano),
		HardExpiresAt:      application.HardExpiresAt.UTC().Format(time.RFC3339Nano),
		IdleTTLSeconds:     int64(application.IdleTTL / time.Second),
		DistinctRuns:       len(application.SeenRunIDs),
		WorkspaceBytes:     application.WorkspaceBytes,
		WorkspacePreserved: application.WorkspacePreserved && application.State != StateDestroyed,
	}
	if !application.LastUsedAt.IsZero() {
		view.LastUsedAt = application.LastUsedAt.UTC().Format(time.RFC3339Nano)
	}
	if !application.IdleExpiresAt.IsZero() {
		view.IdleExpiresAt = application.IdleExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return view
}

func cloneApplication(application Application) Application {
	application.SeenRunIDs = append([]string(nil), application.SeenRunIDs...)
	return application
}

func invalidRequest(message string) *Error {
	return &Error{Code: CodeInvalidRequest, Message: message}
}

func notFoundError() *Error {
	return &Error{Code: CodeApplicationAbsent, Message: "sandbox application not found"}
}

func internalError(message string, cause error) *Error {
	return &Error{Code: CodeInternal, Message: message, Cause: cause}
}

func canceledError(cause error) *Error {
	return &Error{Code: CodeCanceled, Message: "sandbox request canceled", Cause: cause}
}

func fileOperationMessage(operation string, result EngineExecResult) string {
	message := strings.TrimSpace(result.Stderr)
	if message == "" {
		message = operation + " failed"
	}
	return message
}

func completeUTF8Prefix(content string) (string, bool) {
	if utf8.ValidString(content) {
		return content, true
	}
	for trim := 1; trim <= 3 && trim <= len(content); trim++ {
		candidate := content[:len(content)-trim]
		suffix := []byte(content[len(content)-trim:])
		if utf8.ValidString(candidate) && !utf8.FullRune(suffix) {
			return candidate, true
		}
	}
	return "", false
}

const execCommandScript = `set -eu
resolved=$(readlink -f -- "$1")
case "$resolved" in /workspace|/workspace/*) ;; *) echo "cwd escapes /workspace" >&2; exit 73;; esac
[ -d "$resolved" ] || { echo "cwd does not exist" >&2; exit 74; }
cd -- "$resolved"
mkdir -p -- /workspace/.tmp/go
exec /bin/bash -c "$2"`

const writeFileScript = `set -eu
target="$1"
relative=${target#/workspace/}
filename=${relative##*/}
directories=${relative%/*}
resolved_parent=/workspace
if [ "$directories" != "$relative" ]; then
  remaining=$directories
  while [ -n "$remaining" ]; do
    case "$remaining" in
      */*) segment=${remaining%%/*}; remaining=${remaining#*/} ;;
      *) segment=$remaining; remaining= ;;
    esac
    [ -n "$segment" ] || continue
    next="$resolved_parent/$segment"
    [ ! -L "$next" ] || { echo "symbolic-link parent is not allowed" >&2; exit 73; }
    if [ -e "$next" ]; then
      [ -d "$next" ] || { echo "file parent is not a directory" >&2; exit 73; }
    else
      mkdir -- "$next"
    fi
    resolved_parent="$next"
  done
fi
resolved_parent=$(readlink -f -- "$resolved_parent")
case "$resolved_parent/" in /workspace/*) ;; *) echo "path escapes /workspace" >&2; exit 73;; esac
target="$resolved_parent/$filename"
[ ! -L "$target" ] || { echo "symbolic-link destination is not allowed" >&2; exit 73; }
if [ -e "$target" ]; then
  [ -f "$target" ] || { echo "destination is not a regular file" >&2; exit 73; }
fi
if [ "$2" = 1 ]; then
  cat >> "$target"
else
  tmp=$(mktemp "$resolved_parent/.easygo-write.XXXXXX")
  trap 'rm -f -- "$tmp"' EXIT
  cat > "$tmp"
  chmod 0600 "$tmp"
  mv -f -- "$tmp" "$target"
  trap - EXIT
fi
[ "$3" = 1 ] && chmod u+x -- "$target" || true
stat -c %s -- "$target"`

const readFileScript = `set -eu
resolved=$(readlink -f -- "$1")
case "$resolved" in /workspace/*) ;; *) echo "path escapes /workspace" >&2; exit 73;; esac
[ -f "$resolved" ] || { echo "file does not exist" >&2; exit 74; }
size=$(stat -c %s -- "$resolved")
printf '%s\n' "$size"
dd if="$resolved" bs=1 skip="$2" count="$3" status=none`

const readyProbeScript = `set -eu
for reserved in /workspace/.cache /workspace/.cache/go-build /workspace/.cache/go-mod /workspace/.cache/npm /workspace/.cache/python /workspace/.tmp /workspace/.tmp/go /workspace/.home; do
  [ ! -L "$reserved" ] || exit 75
done
mkdir -p /workspace/.cache/go-build /workspace/.cache/go-mod /workspace/.cache/npm /workspace/.cache/python /workspace/.tmp/go /workspace/.home
test -w /workspace && test -w /workspace/.cache && test -w /workspace/.tmp/go && test -w /workspace/.home`

const cleanupUserProcessesScript = `set -eu
self=$$
pass=0
while [ "$pass" -lt 8 ]; do
  found=0
  for proc in /proc/[0-9]*; do
    pid=${proc#/proc/}
    [ "$pid" = "$self" ] && continue
    uid=
    state=
    while read -r key value rest; do
      if [ "$key" = "Uid:" ]; then uid=$value; fi
      if [ "$key" = "State:" ]; then state=$value; fi
    done < "$proc/status" 2>/dev/null || true
    if [ "$uid" = 1000 ] && [ "$state" != Z ]; then
      kill -KILL "$pid" 2>/dev/null || true
      found=1
    fi
  done
  [ "$found" = 1 ] || exit 0
  pass=$((pass + 1))
  sleep 0.01
done
for proc in /proc/[0-9]*; do
  pid=${proc#/proc/}
  [ "$pid" = "$self" ] && continue
  uid=
  state=
  while read -r key value rest; do
    if [ "$key" = "Uid:" ]; then uid=$value; fi
    if [ "$key" = "State:" ]; then state=$value; fi
  done < "$proc/status" 2>/dev/null || true
  [ "$uid" != 1000 ] || [ "$state" = Z ] || exit 76
done`
