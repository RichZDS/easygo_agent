package sandbox

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerRecoversAmbiguousContainerCreateWithoutDuplicate(t *testing.T) {
	tests := []struct {
		name     string
		cancel   bool
		returnID bool
		wantCode string
	}{
		{name: "transport error", wantCode: CodeInternal},
		{name: "transport error with daemon id", returnID: true, wantCode: CodeInternal},
		{name: "request canceled after daemon creates", cancel: true, wantCode: CodeCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, engine, _ := newTestManager(t, testConfig())
			application := mustApply(t, manager, "session-a")
			identity := Identity{SessionID: "session-a", RunID: "run-a"}
			requestContext := context.Background()
			createErr := errors.New("ambiguous create response")
			if test.cancel {
				ctx, cancel := context.WithCancel(context.Background())
				requestContext = ctx
				createErr = context.Canceled
				engine.setCreateHook(cancel)
			}
			engine.setAmbiguousCreateError(createErr, test.returnID)
			_, err := manager.Create(requestContext, identity, application.ID)
			assertSandboxErrorCode(t, err, test.wantCode)

			manager.mu.Lock()
			retainedID := manager.applications[application.ID].ContainerID
			manager.mu.Unlock()
			if retainedID == "" || engine.containerCount() != 1 {
				t.Fatalf("ambiguous create was not retained: id=%q containers=%d", retainedID, engine.containerCount())
			}

			engine.setAmbiguousCreateError(nil, false)
			engine.setCreateHook(nil)
			if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
				t.Fatal(err)
			}
			if engine.containerCount() != 1 || engine.runningCount() != 1 {
				t.Fatalf("retry duplicated recovered container: containers=%d running=%d", engine.containerCount(), engine.runningCount())
			}
		})
	}
}

func TestManagerReleaseStopFailureStaysRetryable(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	engine.setStopError(errors.New("injected stop failure"))
	_, err := manager.Release(context.Background(), identity, application.ID)
	assertSandboxErrorCode(t, err, CodeInternal)
	status, err := manager.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.State != StateHibernating || engine.runningCount() != 1 {
		t.Fatalf("failed release stop freed slot: state=%s running=%d", status.Application.State, engine.runningCount())
	}
	engine.setStopError(nil)
	if err := manager.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.State != StateHibernated || engine.runningCount() != 0 {
		t.Fatalf("reap did not retry release stop: state=%s running=%d", status.Application.State, engine.runningCount())
	}
}

func TestManagerImmediateRetryAfterStopFailureDoesNotWaitForItsOwnSlot(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 1
	cfg.MaxStarting = 1
	cfg.WaitTimeout = minimumWaitTimeout
	manager, engine, _ := newTestManager(t, cfg)
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	engine.setStopError(errors.New("injected stop failure"))
	_, err := manager.Release(context.Background(), identity, application.ID)
	assertSandboxErrorCode(t, err, CodeInternal)

	engine.setStopError(nil)
	retryContext, retryCancel := context.WithTimeout(context.Background(), time.Second)
	defer retryCancel()
	if _, err := manager.Create(retryContext, identity, application.ID); err != nil {
		t.Fatalf("retry should recover its retained running slot: %v", err)
	}
	if engine.runningCount() != 1 || engine.maxObservedRunning() > 1 {
		t.Fatalf("retry running=%d max=%d, want 1/1", engine.runningCount(), engine.maxObservedRunning())
	}
}

func TestManagerLRUStopFailureRetainsVictimSlot(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 1
	cfg.MaxStarting = 1
	manager, engine, _ := newTestManager(t, cfg)
	first := mustApply(t, manager, "session-a")
	second := mustApply(t, manager, "session-b")
	firstIdentity := Identity{SessionID: "session-a", RunID: "run-a"}
	if _, err := manager.Create(context.Background(), firstIdentity, first.ID); err != nil {
		t.Fatal(err)
	}
	engine.setStopError(errors.New("injected LRU stop failure"))
	_, err := manager.Create(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID)
	assertSandboxErrorCode(t, err, CodeInternal)
	status, err := manager.Status(context.Background(), firstIdentity, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.State != StateHibernating || engine.runningCount() != 1 {
		t.Fatalf("failed LRU stop freed victim: state=%s running=%d", status.Application.State, engine.runningCount())
	}
	engine.setStopError(nil)
	if err := manager.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID); err != nil {
		t.Fatal(err)
	}
	if engine.maxObservedRunning() > 1 {
		t.Fatalf("LRU stop retry exceeded capacity: %d", engine.maxObservedRunning())
	}
}

func TestManagerStartFailureDiscardsContainerAndPreservesWorkspace(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	engine.startFn = func(context.Context, string) error { return errors.New("injected start failure") }
	_, err := manager.Create(context.Background(), identity, application.ID)
	assertSandboxErrorCode(t, err, CodeInternal)
	if engine.containerCount() != 0 || engine.runningCount() != 0 || engine.volumeCount() != 1 {
		t.Fatalf("failed start cleanup: containers=%d running=%d volumes=%d", engine.containerCount(), engine.runningCount(), engine.volumeCount())
	}
	manager.mu.Lock()
	retainedID := manager.applications[application.ID].ContainerID
	manager.mu.Unlock()
	if retainedID != "" {
		t.Fatalf("failed start retained disposable container reference %q", retainedID)
	}
	engine.startFn = nil
	if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	if engine.containerCount() != 1 || engine.runningCount() != 1 {
		t.Fatalf("start retry duplicated container: containers=%d running=%d", engine.containerCount(), engine.runningCount())
	}
}

func TestManagerDelayedAmbiguousStartCannotEscapeRunningCap(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 1
	cfg.MaxStarting = 1
	manager, engine, _ := newTestManager(t, cfg)
	first := mustApply(t, manager, "session-a")
	second := mustApply(t, manager, "session-b")

	cleanupInspected := make(chan struct{})
	allowCleanup := make(chan struct{})
	allowLateStart := make(chan struct{})
	lateStartDone := make(chan struct{})
	var inspectOnce sync.Once
	var startCalls atomic.Int32
	engine.inspectHook = func(_ string, info ContainerInfo) {
		if !info.Exists || info.Labels[applicationLabel] != first.ID {
			return
		}
		inspectOnce.Do(func() {
			close(cleanupInspected)
			<-allowCleanup
		})
	}
	engine.startFn = func(_ context.Context, id string) error {
		if startCalls.Add(1) != 1 {
			return nil
		}
		go func() {
			<-allowLateStart
			engine.mu.Lock()
			if container := engine.containers[id]; container != nil {
				container.running = true
				running := 0
				for _, candidate := range engine.containers {
					if candidate.running {
						running++
					}
				}
				if running > engine.maximumRunning {
					engine.maximumRunning = running
				}
			}
			engine.mu.Unlock()
			close(lateStartDone)
		}()
		return errors.New("ambiguous start response")
	}

	firstResult := make(chan error, 1)
	go func() {
		_, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID)
		firstResult <- err
	}()
	<-cleanupInspected
	secondResult := make(chan error, 1)
	go func() {
		_, err := manager.Create(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID)
		secondResult <- err
	}()
	close(allowLateStart)
	<-lateStartDone
	close(allowCleanup)

	assertSandboxErrorCode(t, <-firstResult, CodeInternal)
	if err := <-secondResult; err != nil {
		t.Fatal(err)
	}
	if got := engine.runningCount(); got != 1 {
		t.Fatalf("running containers=%d, want 1", got)
	}
	if got := engine.maxObservedRunning(); got > 1 {
		t.Fatalf("delayed start escaped running cap: observed %d", got)
	}
	if engine.volumeCount() != 2 {
		t.Fatalf("workspace volumes=%d, want 2", engine.volumeCount())
	}
}

func TestManagerReadyFailureAndFailedStopRemainStarting(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	engine.setReadyError(errors.New("not ready"))
	engine.setStopError(errors.New("injected stop failure"))
	requestContext, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err := manager.Create(requestContext, identity, application.ID)
	cancel()
	assertSandboxErrorCode(t, err, CodeInternal)
	manager.mu.Lock()
	record := cloneApplication(*manager.applications[application.ID])
	manager.mu.Unlock()
	if record.State != StateStarting || record.ContainerID == "" || engine.runningCount() != 1 {
		t.Fatalf("failed readiness cleanup freed container: record=%+v running=%d", record, engine.runningCount())
	}
	engine.setReadyError(nil)
	engine.setStopError(nil)
	if err := manager.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.State != StateHibernated || engine.runningCount() != 0 {
		t.Fatalf("reap did not recover readiness failure: state=%s running=%d", status.Application.State, engine.runningCount())
	}
}

func TestManagerPersistFailureAfterCreateKeepsContainerForReap(t *testing.T) {
	cfg := testConfig()
	engine := newFakeEngine()
	clock := newFakeClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	store := newMemoryStore()
	manager, err := NewManager(cfg, engine, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	application := mustApply(t, manager, "session-a")
	failed := false
	store.setPutFunc(func(record Application) error {
		if !failed && record.State == StateStarting && record.ContainerID != "" {
			failed = true
			return errors.New("injected state write failure")
		}
		return nil
	})
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	_, err = manager.Create(context.Background(), identity, application.ID)
	assertSandboxErrorCode(t, err, CodeInternal)
	manager.mu.Lock()
	record := cloneApplication(*manager.applications[application.ID])
	manager.mu.Unlock()
	if record.ContainerID == "" || record.State != StateStarting || engine.containerCount() != 1 {
		t.Fatalf("persist failure lost recoverable state: record=%+v containers=%d", record, engine.containerCount())
	}
	store.setPutFunc(nil)
	if err := manager.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.State != StateHibernated || engine.runningCount() != 0 {
		t.Fatalf("reap did not recover failed activation: state=%s running=%d", status.Application.State, engine.runningCount())
	}
}

func TestManagerReapDiscardsUnexpectedRunningContainerAndCleansUnknownResources(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	applicationView := mustApply(t, manager, "session-a")
	manager.mu.Lock()
	application := cloneApplication(*manager.applications[applicationView.ID])
	manager.mu.Unlock()
	ownedID, err := engine.CreateContainer(context.Background(), manager.containerSpec(&application))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.StartContainer(context.Background(), ownedID); err != nil {
		t.Fatal(err)
	}
	orphanLabels := map[string]string{
		LabelManaged:       "true",
		LabelNamespace:     manager.cfg.Namespace,
		LabelApplicationID: "orphan",
		LabelResource:      "volume",
		LabelHardExpiresAt: application.HardExpiresAt.Format(time.RFC3339Nano),
	}
	if _, err := engine.CreateVolume(context.Background(), "test-workspace-orphan", orphanLabels); err != nil {
		t.Fatal(err)
	}
	orphanLabels[LabelResource] = "container"
	orphanID, err := engine.CreateContainer(context.Background(), ContainerSpec{Name: "test-sandbox-orphan", VolumeName: "test-workspace-orphan", Labels: cloneLabels(orphanLabels)})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.StartContainer(context.Background(), orphanID); err != nil {
		t.Fatal(err)
	}

	if err := manager.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	recovered := cloneApplication(*manager.applications[application.ID])
	manager.mu.Unlock()
	if recovered.ContainerID != "" || recovered.State != StateHibernated {
		t.Fatalf("unexpected running container was not safely discarded: %+v", recovered)
	}
	if engine.containerCount() != 0 || engine.volumeCount() != 1 || engine.runningCount() != 0 {
		t.Fatalf("orphan sweep result: containers=%d volumes=%d running=%d", engine.containerCount(), engine.volumeCount(), engine.runningCount())
	}
}

func TestManagerHibernateAllLockWaitHonorsContext(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	started := make(chan struct{})
	finish := make(chan struct{})
	var once sync.Once
	engine.execFn = func(ctx context.Context, _ string, _ EngineExecRequest) (EngineExecResult, error) {
		once.Do(func() { close(started) })
		<-finish
		return EngineExecResult{}, ctx.Err()
	}
	execDone := make(chan error, 1)
	go func() {
		_, err := manager.Exec(context.Background(), identity, application.ID, ExecRequest{Command: "sleep forever"})
		execDone <- err
	}()
	<-started
	shutdownContext, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err := manager.HibernateAll(shutdownContext)
	cancel()
	close(finish)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HibernateAll error=%v, want context deadline", err)
	}
	select {
	case <-execDone:
	case <-time.After(time.Second):
		t.Fatal("exec did not finish after test released it")
	}
}

func TestManagerDestroyLockWaitHonorsContext(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	started := make(chan struct{})
	finish := make(chan struct{})
	var once sync.Once
	engine.execFn = func(ctx context.Context, _ string, _ EngineExecRequest) (EngineExecResult, error) {
		once.Do(func() { close(started) })
		<-finish
		return EngineExecResult{}, ctx.Err()
	}
	execDone := make(chan error, 1)
	go func() {
		_, err := manager.Exec(context.Background(), identity, application.ID, ExecRequest{Command: "sleep forever"})
		execDone <- err
	}()
	<-started
	destroyContext, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err := manager.Destroy(destroyContext, identity, application.ID)
	cancel()
	if errorCode(err) != CodeCanceled {
		t.Fatalf("Destroy error=%v, want canceled", err)
	}
	close(finish)
	select {
	case <-execDone:
	case <-time.After(time.Second):
		t.Fatal("exec did not finish after test released it")
	}
	if err := manager.Destroy(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManagerCapacityWakeAtHardDeadlineDoesNotDoubleUnlock(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 1
	cfg.MaxStarting = 1
	cfg.BaseIdleTTL = time.Second
	cfg.HardTTL = 10 * time.Second
	manager, engine, clock := newTestManager(t, cfg)
	first := mustApply(t, manager, "session-a")
	second := mustApply(t, manager, "session-b")
	started := make(chan struct{})
	finish := make(chan struct{})
	var once sync.Once
	engine.execFn = func(context.Context, string, EngineExecRequest) (EngineExecResult, error) {
		once.Do(func() { close(started) })
		<-finish
		return EngineExecResult{}, nil
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.Exec(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID, ExecRequest{Command: "busy"})
		firstDone <- err
	}()
	<-started
	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.Create(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID)
		secondDone <- err
	}()
	waitForWaiterCount(t, manager, 1)
	clock.AdvanceWithoutFiring(cfg.HardTTL)
	manager.mu.Lock()
	manager.signalLocked()
	manager.mu.Unlock()
	select {
	case err := <-secondDone:
		assertSandboxErrorCode(t, err, CodeExpired)
	case <-time.After(time.Second):
		t.Fatal("capacity waiter did not observe hard deadline")
	}
	close(finish)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("busy operation did not finish")
	}
}
