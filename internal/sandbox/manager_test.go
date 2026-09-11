package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerApplyIsIdempotentAndIsolatesSessions(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	first, err := manager.Apply(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Application.ID == "" {
		t.Fatalf("first apply=%+v", first)
	}
	repeated, err := manager.Apply(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Created || repeated.Application.ID != first.Application.ID {
		t.Fatalf("repeated apply=%+v, first=%+v", repeated, first)
	}
	second, err := manager.Apply(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Application.ID == first.Application.ID {
		t.Fatal("different sessions received the same application id")
	}
	_, err = manager.Status(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, first.Application.ID)
	assertSandboxErrorCode(t, err, CodeApplicationAbsent)
	if got := engine.volumeCount(); got != 2 {
		t.Fatalf("volume count=%d, want 2", got)
	}
}

func TestManagerApplyReusesOneIDAcrossRunsInTheSameSession(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	first, err := manager.Apply(context.Background(), Identity{SessionID: "session-a", RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Apply(context.Background(), Identity{SessionID: "session-a", RunID: "run-2"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Application.ID != first.Application.ID {
		t.Fatalf("same session different run=%+v, first=%+v", second, first)
	}
	if got := engine.volumeCount(); got != 1 {
		t.Fatalf("volume count=%d, want 1", got)
	}
}

func TestManagerCrossSessionExecAndFilesCannotAffectOwner(t *testing.T) {
	manager, _, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "owner")
	owner := Identity{SessionID: "owner", RunID: "owner-run"}
	attacker := Identity{SessionID: "attacker", RunID: "attacker-run"}
	if _, err := manager.Create(context.Background(), owner, application.ID); err != nil {
		t.Fatal(err)
	}

	_, err := manager.Exec(context.Background(), attacker, application.ID, ExecRequest{Command: "true"})
	assertSandboxErrorCode(t, err, CodeApplicationAbsent)
	_, err = manager.ReadFile(context.Background(), attacker, application.ID, ReadFileRequest{Path: "/workspace/secret.txt"})
	assertSandboxErrorCode(t, err, CodeApplicationAbsent)
	_, err = manager.WriteFile(context.Background(), attacker, application.ID, WriteFileRequest{Path: "/workspace/pwned.txt", Content: "nope"})
	assertSandboxErrorCode(t, err, CodeApplicationAbsent)

	executed, err := manager.Exec(context.Background(), owner, application.ID, ExecRequest{Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if executed.Application.State != StateWarmIdle {
		t.Fatalf("owner exec state=%q", executed.Application.State)
	}
}

func TestManagerApplyDoesNotHoldGlobalLockDuringVolumeProvision(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	first := mustApply(t, manager, "session-a")
	started := make(chan struct{})
	unblock := make(chan struct{})
	engine.mu.Lock()
	engine.createVolumeFn = func(ctx context.Context, name string, labels map[string]string) (bool, error) {
		close(started)
		select {
		case <-unblock:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		engine.mu.Lock()
		defer engine.mu.Unlock()
		engine.volumes[name] = cloneLabels(labels)
		return true, nil
	}
	engine.mu.Unlock()

	applyDone := make(chan error, 1)
	go func() {
		_, err := manager.Apply(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"})
		applyDone <- err
	}()
	<-started

	statusDone := make(chan error, 1)
	go func() {
		_, err := manager.Status(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID)
		statusDone <- err
	}()
	select {
	case err := <-statusDone:
		if err != nil {
			close(unblock)
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		close(unblock)
		<-applyDone
		t.Fatal("a blocked Docker volume create held the global Manager lock")
	}

	destroyContext, destroyCancel := context.WithTimeout(context.Background(), time.Second)
	if err := manager.Destroy(destroyContext, Identity{SessionID: "session-a", RunID: "run-a"}, first.ID); err != nil {
		destroyCancel()
		close(unblock)
		<-applyDone
		t.Fatalf("destroy existing application during another apply: %v", err)
	}
	destroyCancel()
	close(unblock)
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
}

func TestManagerConcurrentApplyWaitsForProvisioningAndNeverReturnsRolledBackID(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	started := make(chan struct{})
	unblock := make(chan struct{})
	var calls atomic.Int32
	engine.mu.Lock()
	engine.createVolumeFn = func(ctx context.Context, name string, labels map[string]string) (bool, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-unblock:
				return false, errors.New("injected first provision failure")
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		engine.mu.Lock()
		defer engine.mu.Unlock()
		engine.volumes[name] = cloneLabels(labels)
		return true, nil
	}
	engine.mu.Unlock()

	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.Apply(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"})
		firstDone <- err
	}()
	<-started

	type applyOutcome struct {
		result ApplyResult
		err    error
	}
	secondDone := make(chan applyOutcome, 1)
	go func() {
		result, err := manager.Apply(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"})
		secondDone <- applyOutcome{result: result, err: err}
	}()
	select {
	case outcome := <-secondDone:
		t.Fatalf("concurrent Apply returned before provisioning completed: result=%+v err=%v", outcome.result, outcome.err)
	case <-time.After(25 * time.Millisecond):
	}

	close(unblock)
	if err := <-firstDone; err == nil {
		t.Fatal("first Apply succeeded despite injected volume failure")
	}
	outcome := <-secondDone
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if !outcome.result.Created || outcome.result.Application.ID == "" {
		t.Fatalf("second Apply did not create a fresh application: %+v", outcome.result)
	}
	if got := engine.volumeCount(); got != 1 {
		t.Fatalf("volume count=%d, want 1", got)
	}
}

func TestManagerApplyCrossingHardDeadlineDestroysProvisionedWorkspace(t *testing.T) {
	cfg := testConfig()
	cfg.BaseIdleTTL = time.Second
	cfg.HardTTL = 2 * time.Second
	cfg.ReaperInterval = time.Second
	manager, engine, clock := newTestManager(t, cfg)
	engine.mu.Lock()
	engine.createVolumeFn = func(_ context.Context, name string, labels map[string]string) (bool, error) {
		engine.mu.Lock()
		engine.volumes[name] = cloneLabels(labels)
		engine.mu.Unlock()
		clock.Advance(cfg.HardTTL)
		return true, nil
	}
	engine.mu.Unlock()

	_, err := manager.Apply(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"})
	assertSandboxErrorCode(t, err, CodeExpired)
	if got := engine.volumeCount(); got != 0 {
		t.Fatalf("volume count=%d after expired Apply, want 0", got)
	}
	manager.mu.Lock()
	applications, sessions := len(manager.applications), len(manager.bySession)
	manager.mu.Unlock()
	if applications != 0 || sessions != 0 {
		t.Fatalf("expired Apply remains registered: applications=%d sessions=%d", applications, sessions)
	}
}

func TestManagerApplyProvisionTimeoutAtHardDeadlineIsExpiredAndLeavesNoState(t *testing.T) {
	cfg := testConfig()
	cfg.BaseIdleTTL = time.Second
	cfg.HardTTL = time.Second
	cfg.ReaperInterval = time.Second
	engine := newFakeEngine()
	engine.createVolumeFn = func(ctx context.Context, _ string, _ map[string]string) (bool, error) {
		<-ctx.Done()
		return false, ctx.Err()
	}
	manager, err := NewManager(cfg, engine, newMemoryStore(), realClock{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	_, err = manager.Apply(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"})
	assertSandboxErrorCode(t, err, CodeExpired)
	if got := engine.volumeCount(); got != 0 {
		t.Fatalf("volume count=%d after hard-deadline provisioning timeout, want 0", got)
	}
	manager.mu.Lock()
	applications := len(manager.applications)
	manager.mu.Unlock()
	if applications != 0 {
		t.Fatalf("hard-deadline provisioning timeout retained %d applications", applications)
	}
}

func TestManagerCanceledApplyProvisionLeavesNoState(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	started := make(chan struct{})
	engine.mu.Lock()
	engine.createVolumeFn = func(ctx context.Context, _ string, _ map[string]string) (bool, error) {
		close(started)
		<-ctx.Done()
		return false, ctx.Err()
	}
	engine.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := manager.Apply(ctx, Identity{SessionID: "session-a", RunID: "run-a"})
		done <- err
	}()
	<-started
	cancel()
	assertSandboxErrorCode(t, <-done, CodeCanceled)
	if got := engine.volumeCount(); got != 0 {
		t.Fatalf("volume count=%d after canceled Apply, want 0", got)
	}
	manager.mu.Lock()
	applications, sessions := len(manager.applications), len(manager.bySession)
	manager.mu.Unlock()
	if applications != 0 || sessions != 0 {
		t.Fatalf("canceled Apply remains registered: applications=%d sessions=%d", applications, sessions)
	}
}

func TestManagerIdleTTLDoublesOnlyForANewRun(t *testing.T) {
	manager, _, clock := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	created, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-1"}, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if created.Application.IdleTTLSeconds != 60 || created.Application.DistinctRuns != 1 {
		t.Fatalf("first run=%+v", created.Application)
	}
	hardExpiry := created.Application.HardExpiresAt
	clock.Advance(10 * time.Second)
	reused, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-1"}, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reused.Application.IdleTTLSeconds != 60 || reused.Application.DistinctRuns != 1 {
		t.Fatalf("same run=%+v", reused.Application)
	}
	clock.Advance(10 * time.Second)
	nextRun, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-2"}, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if nextRun.Application.IdleTTLSeconds != 120 || nextRun.Application.DistinctRuns != 2 {
		t.Fatalf("second run=%+v", nextRun.Application)
	}
	if nextRun.Application.HardExpiresAt != hardExpiry {
		t.Fatalf("hard expiry changed from %s to %s", hardExpiry, nextRun.Application.HardExpiresAt)
	}
}

func TestManagerSuccessfulSameRunExecRefreshesIdleDeadlineWithoutDoubling(t *testing.T) {
	manager, _, clock := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-1"}
	created, err := manager.Create(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstIdle, err := time.Parse(time.RFC3339Nano, created.Application.IdleExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(10 * time.Second)
	executed, err := manager.Exec(context.Background(), identity, application.ID, ExecRequest{Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if executed.Application.State != StateWarmIdle {
		t.Fatalf("exec state=%q, want %s", executed.Application.State, StateWarmIdle)
	}
	if executed.Application.IdleTTLSeconds != 60 || executed.Application.DistinctRuns != 1 {
		t.Fatalf("same-run exec renewed a new TTL band: %+v", executed.Application)
	}
	refreshedIdle, err := time.Parse(time.RFC3339Nano, executed.Application.IdleExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshedIdle.After(firstIdle) {
		t.Fatalf("same-run exec did not refresh idle deadline: first=%s refreshed=%s", firstIdle, refreshedIdle)
	}
	if executed.Application.HardExpiresAt != created.Application.HardExpiresAt {
		t.Fatalf("hard expiry changed from %s to %s", created.Application.HardExpiresAt, executed.Application.HardExpiresAt)
	}
}

func TestManagerStatusDoesNotRenewIdleLease(t *testing.T) {
	manager, _, clock := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-1"}
	created, err := manager.Create(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(15 * time.Second)
	status, err := manager.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.State != created.Application.State {
		t.Fatalf("status changed state from %q to %q", created.Application.State, status.Application.State)
	}
	if status.Application.IdleExpiresAt != created.Application.IdleExpiresAt || status.Application.LastUsedAt != created.Application.LastUsedAt || status.Application.DistinctRuns != created.Application.DistinctRuns || status.Application.IdleTTLSeconds != created.Application.IdleTTLSeconds {
		t.Fatalf("status renewed lease: created=%+v status=%+v", created.Application, status.Application)
	}
}

func TestManagerIdleTTLReapHibernatesAndKeepsVolume(t *testing.T) {
	manager, engine, clock := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-1"}
	if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	executed, err := manager.Exec(context.Background(), identity, application.ID, ExecRequest{Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if executed.Application.State != StateWarmIdle {
		t.Fatalf("pre-reap state=%q", executed.Application.State)
	}
	clock.Advance(time.Minute)
	if err := manager.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.State != StateHibernated {
		t.Fatalf("idle reap state=%q, want %s", status.Application.State, StateHibernated)
	}
	if engine.volumeCount() != 1 || engine.runningCount() != 0 {
		t.Fatalf("idle reap should stop compute and keep the volume: volumes=%d running=%d containers=%d", engine.volumeCount(), engine.runningCount(), engine.containerCount())
	}
}

func TestManagerRestoresApplicationAcrossTTLConfigChanges(t *testing.T) {
	oldConfig := testConfig()
	oldConfig.BaseIdleTTL = time.Hour
	statePath := t.TempDir() + "/state.db"
	store, err := OpenBoltStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	engine := newFakeEngine()
	clock := newFakeClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	oldManager, err := NewManager(oldConfig, engine, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	application := mustApply(t, oldManager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-1"}
	first, err := oldManager.Create(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity.RunID = "run-2"
	second, err := oldManager.Create(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Application.IdleTTLSeconds != int64((2*time.Hour)/time.Second) {
		t.Fatalf("old idle ttl=%d", second.Application.IdleTTLSeconds)
	}
	if err := oldManager.Close(); err != nil {
		t.Fatal(err)
	}

	newConfig := oldConfig
	newConfig.BaseIdleTTL = time.Minute
	newConfig.HardTTL = 30 * time.Minute
	store, err = OpenBoltStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(newConfig, engine, store, clock)
	if err != nil {
		_ = store.Close()
		t.Fatalf("restore application created under old TTL config: %v", err)
	}
	defer restarted.Close()
	if err := restarted.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := restarted.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.HardExpiresAt != first.Application.HardExpiresAt {
		t.Fatalf("absolute deadline changed: old=%s new=%s", first.Application.HardExpiresAt, status.Application.HardExpiresAt)
	}
	if status.Application.IdleTTLSeconds != int64((30*time.Minute)/time.Second) {
		t.Fatalf("restored idle ttl=%d, want current safe cap", status.Application.IdleTTLSeconds)
	}
	identity.RunID = "run-3"
	resumed, err := restarted.Create(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Application.IdleTTLSeconds != int64((30*time.Minute)/time.Second) || resumed.Application.DistinctRuns != 3 {
		t.Fatalf("resumed application=%+v", resumed.Application)
	}
}

func TestManagerCapacityPressureHibernatesLeastRecentlyUsedIdleContainer(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 1
	cfg.MaxStarting = 1
	manager, engine, clock := newTestManager(t, cfg)
	first := mustApply(t, manager, "session-a")
	second := mustApply(t, manager, "session-b")
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID); err != nil {
		t.Fatal(err)
	}
	firstStatus, err := manager.Status(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstStatus.Application.State != StateHibernated {
		t.Fatalf("first state=%q", firstStatus.Application.State)
	}
	secondStatus, err := manager.Status(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondStatus.Application.State != StateActive {
		t.Fatalf("second state=%q", secondStatus.Application.State)
	}
	if got := engine.maxObservedRunning(); got > 1 {
		t.Fatalf("observed %d running containers with max_running=1", got)
	}
	if engine.stopCount() != 1 {
		t.Fatalf("stop count=%d, want 1", engine.stopCount())
	}
}

func TestManagerDefaultThreeSlotsHibernateTheLeastRecentlyUsedIdleContainer(t *testing.T) {
	cfg := testConfig()
	if cfg.MaxRunning != 3 {
		t.Fatalf("test config max_running=%d, want default 3", cfg.MaxRunning)
	}
	manager, engine, clock := newTestManager(t, cfg)
	var applications []ApplicationView
	for index, sessionID := range []string{"session-a", "session-b", "session-c", "session-d"} {
		application := mustApply(t, manager, sessionID)
		applications = append(applications, application)
		if _, err := manager.Create(context.Background(), Identity{SessionID: sessionID, RunID: fmt.Sprintf("run-%d", index)}, application.ID); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Second)
	}

	states := make([]string, 0, len(applications))
	for index, application := range applications {
		status, err := manager.Status(context.Background(), Identity{SessionID: []string{"session-a", "session-b", "session-c", "session-d"}[index], RunID: "status"}, application.ID)
		if err != nil {
			t.Fatal(err)
		}
		states = append(states, status.Application.State)
	}
	if states[0] != StateHibernated {
		t.Fatalf("oldest application state=%q, want %s; states=%v", states[0], StateHibernated, states)
	}
	for index := 1; index < 4; index++ {
		if states[index] != StateActive {
			t.Fatalf("application %d state=%q, want %s; states=%v", index, states[index], StateActive, states)
		}
	}
	if got := engine.maxObservedRunning(); got > 3 {
		t.Fatalf("observed %d running containers with default max_running=3", got)
	}
	if engine.runningCount() != 3 {
		t.Fatalf("running=%d, want 3", engine.runningCount())
	}
	if engine.volumeCount() != 4 {
		t.Fatalf("hibernated workspace was deleted: volumes=%d", engine.volumeCount())
	}
}

func TestManagerCapacityWaiterWakesWhenBusyOperationUnlocks(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 1
	cfg.MaxStarting = 1
	manager, engine, _ := newTestManager(t, cfg)
	first := mustApply(t, manager, "session-a")
	second := mustApply(t, manager, "session-b")
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finish := make(chan struct{})
	var once sync.Once
	engine.execFn = func(ctx context.Context, _ string, request EngineExecRequest) (EngineExecResult, error) {
		once.Do(func() { close(started) })
		select {
		case <-finish:
			return EngineExecResult{}, nil
		case <-ctx.Done():
			return EngineExecResult{}, ctx.Err()
		}
	}
	execDone := make(chan error, 1)
	go func() {
		_, err := manager.Exec(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID, ExecRequest{Command: "true"})
		execDone <- err
	}()
	<-started
	createDone := make(chan error, 1)
	go func() {
		_, err := manager.Create(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID)
		createDone <- err
	}()
	close(finish)
	select {
	case err := <-execDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("busy exec did not finish")
	}
	select {
	case err := <-createDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("capacity waiter was not notified after operation unlock")
	}
}

func TestManagerExecWaitsForControllerHibernationTransition(t *testing.T) {
	manager, _, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}

	manager.mu.Lock()
	operation := manager.operationMu[application.ID]
	applicationRecord := manager.applications[application.ID]
	manager.mu.Unlock()
	operation.Lock()
	manager.mu.Lock()
	applicationRecord.State = StateHibernating
	if err := manager.persistLocked(applicationRecord); err != nil {
		manager.mu.Unlock()
		operation.Unlock()
		t.Fatal(err)
	}
	manager.signalLocked()
	manager.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := manager.Exec(context.Background(), identity, application.ID, ExecRequest{Command: "true"})
		done <- err
	}()
	select {
	case err := <-done:
		operation.Unlock()
		t.Fatalf("exec returned during controller hibernation: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	manager.mu.Lock()
	applicationRecord.State = StateHibernated
	if err := manager.persistLocked(applicationRecord); err != nil {
		manager.mu.Unlock()
		operation.Unlock()
		t.Fatal(err)
	}
	manager.signalLocked()
	manager.mu.Unlock()
	manager.unlockOperation(operation)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("exec did not resume after controller hibernation")
	}
}

func TestManagerFailedStopKeepsRunningSlotUntilReapRetries(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	var once sync.Once
	engine.execFn = func(ctx context.Context, _ string, _ EngineExecRequest) (EngineExecResult, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return EngineExecResult{ExitCode: 0}, nil
	}
	engine.setStopError(errors.New("injected stop failure"))
	execContext, cancelExec := context.WithCancel(context.Background())
	execDone := make(chan error, 1)
	go func() {
		_, err := manager.Exec(execContext, identity, application.ID, ExecRequest{Command: "sleep forever"})
		execDone <- err
	}()
	<-started
	cancelExec()
	assertSandboxErrorCode(t, <-execDone, CodeInternal)
	status, err := manager.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.State != StateBusy || engine.runningCount() != 1 {
		t.Fatalf("failed stop released capacity: state=%s running=%d", status.Application.State, engine.runningCount())
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
		t.Fatalf("reap did not retry stop: state=%s running=%d", status.Application.State, engine.runningCount())
	}
}

func TestManagerFailedRemoveKeepsResourcesAndCapacityUntilReapRetries(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 1
	cfg.MaxStarting = 1
	manager, engine, _ := newTestManager(t, cfg)
	first := mustApply(t, manager, "session-a")
	firstIdentity := Identity{SessionID: "session-a", RunID: "run-a"}
	if _, err := manager.Create(context.Background(), firstIdentity, first.ID); err != nil {
		t.Fatal(err)
	}
	engine.setRemoveContainerError(errors.New("injected remove failure"))
	assertSandboxErrorCode(t, manager.Destroy(context.Background(), firstIdentity, first.ID), CodeInternal)
	if engine.runningCount() != 1 || engine.containerCount() != 1 || engine.volumeCount() != 1 {
		t.Fatalf("failed container removal released resources: running=%d containers=%d volumes=%d", engine.runningCount(), engine.containerCount(), engine.volumeCount())
	}

	second := mustApply(t, manager, "session-b")
	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.Create(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID)
		secondDone <- err
	}()
	waitForWaiterCount(t, manager, 1)
	engine.setRemoveContainerError(nil)
	if err := manager.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if engine.maxObservedRunning() > 1 {
		t.Fatalf("running cap exceeded while removal was pending: %d", engine.maxObservedRunning())
	}
	if _, err := manager.Status(context.Background(), firstIdentity, first.ID); errorCode(err) != CodeApplicationAbsent {
		t.Fatalf("destroyed application remains visible: %v", err)
	}
}

func TestManagerCapacityWaitersAreAdmittedFIFO(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 1
	cfg.MaxStarting = 1
	manager, engine, _ := newTestManager(t, cfg)
	first := mustApply(t, manager, "session-a")
	second := mustApply(t, manager, "session-b")
	third := mustApply(t, manager, "session-c")
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID); err != nil {
		t.Fatal(err)
	}
	started, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	engine.execFn = func(ctx context.Context, _ string, _ EngineExecRequest) (EngineExecResult, error) {
		once.Do(func() { close(started) })
		select {
		case <-finish:
			return EngineExecResult{}, nil
		case <-ctx.Done():
			return EngineExecResult{}, ctx.Err()
		}
	}
	execDone := make(chan error, 1)
	go func() {
		_, err := manager.Exec(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID, ExecRequest{Command: "true"})
		execDone <- err
	}()
	<-started
	create := func(session, run, id string) <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := manager.Create(context.Background(), Identity{SessionID: session, RunID: run}, id)
			done <- err
		}()
		return done
	}
	secondDone := create("session-b", "run-b", second.ID)
	waitForWaiterCount(t, manager, 1)
	thirdDone := create("session-c", "run-c", third.ID)
	waitForWaiterCount(t, manager, 2)
	close(finish)
	for _, result := range []<-chan error{execDone, secondDone, thirdDone} {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	order := engine.startApplicationOrder()
	if len(order) < 3 || order[len(order)-2] != second.ID || order[len(order)-1] != third.ID {
		t.Fatalf("container start order=%v, want second then third", order)
	}
}

func TestManagerCrossSessionReleaseAndDestroyCannotAffectOwner(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "owner")
	if _, err := manager.Create(context.Background(), Identity{SessionID: "owner", RunID: "owner-run"}, application.ID); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Release(context.Background(), Identity{SessionID: "attacker", RunID: "attacker-run"}, application.ID)
	assertSandboxErrorCode(t, err, CodeApplicationAbsent)
	if err := manager.Destroy(context.Background(), Identity{SessionID: "attacker", RunID: "attacker-run"}, application.ID); err != nil {
		t.Fatalf("cross-session destroy must be indistinguishable from absent: %v", err)
	}
	status, err := manager.Status(context.Background(), Identity{SessionID: "owner", RunID: "owner-run"}, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.State != StateActive || engine.stopCount() != 0 {
		t.Fatalf("owner application was affected: status=%+v stops=%d", status.Application, engine.stopCount())
	}
}

func TestManagerFailedFileRequestDoesNotRenewLease(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	engine.execFn = func(_ context.Context, _ string, request EngineExecRequest) (EngineExecResult, error) {
		return EngineExecResult{ExitCode: 74, Stderr: "file does not exist"}, nil
	}
	_, err := manager.ReadFile(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, application.ID, ReadFileRequest{Path: "/workspace/missing"})
	assertSandboxErrorCode(t, err, CodeInvalidRequest)
	status, err := manager.Status(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.DistinctRuns != 0 || status.Application.LastUsedAt != "" || status.Application.State != StateHibernated {
		t.Fatalf("failed read renewed lease: %+v", status.Application)
	}
}

func TestManagerHardDeadlineDestroysContainerVolumeAndState(t *testing.T) {
	manager, engine, clock := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, application.ID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Hour)
	if err := manager.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.volumeCount() != 0 || engine.containerCount() != 0 {
		t.Fatalf("resources remain after hard deadline: volumes=%d containers=%d", engine.volumeCount(), engine.containerCount())
	}
	replacement := mustApply(t, manager, "session-a")
	if replacement.ID == application.ID {
		t.Fatal("destroyed application id was reused")
	}
}

func TestManagerCapacityWaitIsClampedToApplicationHardDeadline(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 1
	cfg.MaxStarting = 1
	cfg.BaseIdleTTL = time.Second
	cfg.HardTTL = 10 * time.Second
	manager, engine, clock := newTestManager(t, cfg)
	first := mustApply(t, manager, "session-a")
	second := mustApply(t, manager, "session-b")
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID); err != nil {
		t.Fatal(err)
	}
	started, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	engine.execFn = func(ctx context.Context, _ string, _ EngineExecRequest) (EngineExecResult, error) {
		once.Do(func() { close(started) })
		select {
		case <-finish:
			return EngineExecResult{}, nil
		case <-ctx.Done():
			return EngineExecResult{}, ctx.Err()
		}
	}
	execDone := make(chan error, 1)
	go func() {
		_, err := manager.Exec(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID, ExecRequest{Command: "true"})
		execDone <- err
	}()
	<-started
	waitDone := make(chan error, 1)
	go func() {
		_, err := manager.Create(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID)
		waitDone <- err
	}()
	clock.WaitForAfter(t, 10*time.Second)
	clock.Advance(10 * time.Second)
	assertSandboxErrorCode(t, <-waitDone, CodeExpired)
	if _, err := manager.Status(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID); errorCode(err) != CodeApplicationAbsent {
		t.Fatalf("expired waiter remains addressable: %v", err)
	}
	close(finish)
	assertSandboxErrorCode(t, <-execDone, CodeExpired)
}

func TestManagerSlowStartCrossingHardDeadlineDestroysResources(t *testing.T) {
	cfg := testConfig()
	cfg.BaseIdleTTL = time.Second
	cfg.HardTTL = 5 * time.Second
	cfg.ReaperInterval = time.Second
	manager, engine, clock := newTestManager(t, cfg)
	application := mustApply(t, manager, "session-a")
	engine.startFn = func(context.Context, string) error {
		clock.Advance(5 * time.Second)
		return nil
	}
	_, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, application.ID)
	assertSandboxErrorCode(t, err, CodeExpired)
	if engine.containerCount() != 0 || engine.volumeCount() != 0 {
		t.Fatalf("resources remain after activation crossed deadline: containers=%d volumes=%d", engine.containerCount(), engine.volumeCount())
	}
}

func TestRunReaperReschedulesForApplicationHardDeadline(t *testing.T) {
	cfg := testConfig()
	cfg.BaseIdleTTL = time.Minute
	cfg.HardTTL = 5 * time.Hour
	cfg.ReaperInterval = 5 * time.Hour
	manager, engine, clock := newTestManager(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.RunReaper(ctx) }()
	clock.WaitForAfter(t, 5*time.Hour)

	clock.Advance(time.Hour)
	application := mustApply(t, manager, "session-a")
	clock.WaitForAfter(t, 5*time.Hour)
	clock.WaitForStableTimerCount(t, 1)
	clock.Advance(4 * time.Hour)
	if engine.volumeCount() != 1 {
		cancel()
		t.Fatalf("application was removed before its hard deadline: %s", application.ID)
	}
	clock.Advance(time.Hour)
	deadline := time.After(time.Second)
	for engine.volumeCount() != 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("reaper did not destroy the application at its hard deadline")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunReaper did not stop after cancellation")
	}
	if count := clock.activeTimerCount(); count != 0 {
		t.Fatalf("reaper retained %d timers after cancellation", count)
	}
}

func TestRunReaperStopsWhenManagerCloses(t *testing.T) {
	manager, _, clock := newTestManager(t, testConfig())
	done := make(chan error, 1)
	go func() { done <- manager.RunReaper(context.Background()) }()
	clock.WaitForAfter(t, manager.cfg.ReaperInterval)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunReaper did not stop when Manager closed")
	}
	if count := clock.activeTimerCount(); count != 0 {
		t.Fatalf("reaper retained %d timers after Manager.Close", count)
	}
}

func TestRunReaperBacksOffWhenDueApplicationOperationIsLocked(t *testing.T) {
	manager, _, clock := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, application.ID); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	operation := manager.operationMu[application.ID]
	manager.mu.Unlock()
	operation.Lock()
	clock.Advance(manager.cfg.BaseIdleTTL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.RunReaper(ctx) }()
	clock.WaitForAfter(t, time.Second)
	clock.WaitForStableTimerCount(t, 1)

	operation.Unlock()
	manager.mu.Lock()
	manager.signalLocked()
	manager.mu.Unlock()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunReaper did not stop after cancellation")
	}
}

func TestReapCancelsHardExpiredExecBeforeDockerEnumeration(t *testing.T) {
	manager, engine, clock := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	execContext, cancelExec := context.WithCancel(context.Background())
	defer cancelExec()
	listStarted := make(chan bool, 1)
	unblockList := make(chan struct{})
	engine.mu.Lock()
	engine.listManagedFn = func(context.Context, string) (ManagedResources, error) {
		canceledBeforeList := false
		select {
		case <-execContext.Done():
			canceledBeforeList = true
		default:
		}
		listStarted <- canceledBeforeList
		<-unblockList
		return ManagedResources{}, nil
	}
	engine.mu.Unlock()
	manager.mu.Lock()
	manager.applications[application.ID].State = StateBusy
	manager.execCancels[application.ID] = cancelExec
	manager.mu.Unlock()
	clock.Advance(manager.cfg.HardTTL)

	done := make(chan error, 1)
	go func() { done <- manager.Reap(context.Background()) }()
	if canceledBeforeList := <-listStarted; !canceledBeforeList {
		close(unblockList)
		<-done
		t.Fatal("hard-expired execution was not canceled before Docker enumeration")
	}
	close(unblockList)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestManagerIdleTTLStopsAtFiveHoursAndNeverExtendsHardDeadline(t *testing.T) {
	manager, _, clock := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	var latest ApplicationResult
	for run := 1; run <= 12; run++ {
		var err error
		latest, err = manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: fmt.Sprintf("run-%d", run)}, application.ID)
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Second)
	}
	if latest.Application.IdleTTLSeconds != int64((5*time.Hour)/time.Second) {
		t.Fatalf("idle ttl=%d", latest.Application.IdleTTLSeconds)
	}
	idleDeadline, err := time.Parse(time.RFC3339Nano, latest.Application.IdleExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	hardDeadline, err := time.Parse(time.RFC3339Nano, latest.Application.HardExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if idleDeadline.After(hardDeadline) {
		t.Fatalf("idle deadline %s exceeds hard deadline %s", idleDeadline, hardDeadline)
	}
}

func TestManagerLostVolumeIsRecreatedButReportedNotPreserved(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Release(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	engine.loseAllResources()
	result, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-b"}, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Application.WorkspacePreserved {
		t.Fatalf("lost workspace reported preserved: %+v", result.Application)
	}
	if engine.volumeCount() != 1 || engine.containerCount() != 1 {
		t.Fatalf("resources were not rebuilt: volumes=%d containers=%d", engine.volumeCount(), engine.containerCount())
	}
}

func TestManagerReconcileDropsInheritedContainersKeepsVolumeAndCleansOrphans(t *testing.T) {
	cfg := testConfig()
	store := newMemoryStore()
	engine := newFakeEngine()
	clock := newFakeClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	first, err := NewManager(cfg, engine, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	application := mustApply(t, first, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	if _, err := first.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	orphanLabels := map[string]string{LabelManaged: "true", LabelNamespace: cfg.Namespace, LabelApplicationID: "orphan", LabelResource: "volume", LabelHardExpiresAt: clock.Now().Add(time.Hour).Format(time.RFC3339Nano)}
	if _, err := engine.CreateVolume(context.Background(), "test-workspace-orphan", orphanLabels); err != nil {
		t.Fatal(err)
	}
	orphanLabels[LabelResource] = "container"
	orphanContainer, err := engine.CreateContainer(context.Background(), ContainerSpec{VolumeName: "test-workspace-orphan", Labels: cloneLabels(orphanLabels)})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.StartContainer(context.Background(), orphanContainer); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewManager(cfg, engine, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := restarted.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.State != StateHibernated || !status.Application.WorkspacePreserved {
		t.Fatalf("reconciled status=%+v", status.Application)
	}
	if engine.containerCount() != 0 || engine.volumeCount() != 1 {
		t.Fatalf("reconciled resources: containers=%d volumes=%d", engine.containerCount(), engine.volumeCount())
	}
	if _, err := restarted.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-b"}, application.ID); err != nil {
		t.Fatal(err)
	}
	if engine.containerCount() != 1 || engine.volumeCount() != 1 {
		t.Fatalf("resume resources: containers=%d volumes=%d", engine.containerCount(), engine.volumeCount())
	}
}

func TestManagerReconcileRefusesToReplaceExpectedVolumeWithWrongLabels(t *testing.T) {
	cfg := testConfig()
	store := newMemoryStore()
	engine := newFakeEngine()
	clock := newFakeClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	first, err := NewManager(cfg, engine, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	application := mustApply(t, first, "session-a")
	engine.corruptVolumeLabels(application.ID, map[string]string{
		LabelManaged:       "true",
		LabelNamespace:     cfg.Namespace,
		LabelApplicationID: application.ID,
		LabelResource:      "volume",
		LabelHardExpiresAt: "wrong-deadline",
	})
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(cfg, engine, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.Reconcile(context.Background()); err == nil {
		t.Fatal("reconcile replaced a workspace whose ownership labels were inconsistent")
	}
	if engine.volumeCount() != 1 {
		t.Fatalf("mismatched workspace was deleted; volume count=%d", engine.volumeCount())
	}
}

func TestManagerStatusFailsClosedWhenStorageCannotBeMeasured(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	engine.setStorageUsageError(errors.New("usage unavailable"))
	_, err := manager.Status(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, application.ID)
	assertSandboxErrorCode(t, err, CodeInternal)
}

func TestManagerExecStopsWhenWorkspaceCrossesSoftLimit(t *testing.T) {
	cfg := testConfig()
	cfg.WorkspaceSoftLimit = 100
	manager, engine, clock := newTestManager(t, cfg)
	application := mustApply(t, manager, "session-a")
	started := make(chan struct{})
	var once sync.Once
	engine.execFn = func(ctx context.Context, _ string, _ EngineExecRequest) (EngineExecResult, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return EngineExecResult{ExitCode: 0}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := manager.Exec(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, application.ID, ExecRequest{Command: "while true; do :; done"})
		done <- err
	}()
	<-started
	clock.WaitForAfter(t, time.Second)
	engine.setStorageUsage(101, 101, 1<<50)
	clock.Advance(time.Second)
	assertSandboxErrorCode(t, <-done, CodeStoragePressure)
	if engine.runningCount() != 0 || engine.volumeCount() != 1 {
		t.Fatalf("storage-pressure cleanup: running=%d volumes=%d, want 0/1", engine.runningCount(), engine.volumeCount())
	}
}

func TestManagerExecGlobalStoragePressureCancelsConcurrentCommands(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 2
	cfg.MaxStarting = 2
	cfg.TotalWorkspaceLimit = 100
	cfg.WorkspaceSoftLimit = 100
	manager, engine, clock := newTestManager(t, cfg)
	applications := []ApplicationView{
		mustApply(t, manager, "session-a"),
		mustApply(t, manager, "session-b"),
	}
	started := make(chan struct{}, len(applications))
	engine.execFn = func(ctx context.Context, _ string, _ EngineExecRequest) (EngineExecResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return EngineExecResult{}, ctx.Err()
	}
	done := make(chan error, len(applications))
	for index, application := range applications {
		index, application := index, application
		go func() {
			identity := Identity{SessionID: fmt.Sprintf("session-%c", 'a'+index), RunID: fmt.Sprintf("run-%d", index)}
			_, err := manager.Exec(context.Background(), identity, application.ID, ExecRequest{Command: "while true; do :; done"})
			done <- err
		}()
	}
	for range applications {
		<-started
		clock.WaitForAfter(t, time.Second)
	}
	var pressureOnce sync.Once
	engine.setStorageUsageFunc(func(context.Context, string) (StorageUsage, error) {
		usage := StorageUsage{WorkspaceBytes: map[string]int64{}, FreeBytes: 1 << 50}
		pressureOnce.Do(func() { usage.TotalBytes = 101 })
		return usage, nil
	})
	clock.Advance(time.Second)
	for range applications {
		select {
		case err := <-done:
			assertSandboxErrorCode(t, err, CodeStoragePressure)
		case <-time.After(time.Second):
			t.Fatal("global storage pressure did not cancel every concurrent command")
		}
	}
	if engine.runningCount() != 0 || engine.volumeCount() != len(applications) {
		t.Fatalf("global storage-pressure cleanup: running=%d volumes=%d, want 0/%d", engine.runningCount(), engine.volumeCount(), len(applications))
	}
}

func TestManagerCapacityTimeoutAndCanceledWaiterLeaveNoQueueResidue(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRunning = 1
	cfg.MaxStarting = 1
	manager, engine, clock := newTestManager(t, cfg)
	first := mustApply(t, manager, "session-a")
	second := mustApply(t, manager, "session-b")
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finish := make(chan struct{})
	var once sync.Once
	engine.execFn = func(ctx context.Context, _ string, _ EngineExecRequest) (EngineExecResult, error) {
		once.Do(func() { close(started) })
		select {
		case <-finish:
			return EngineExecResult{}, nil
		case <-ctx.Done():
			return EngineExecResult{}, ctx.Err()
		}
	}
	execDone := make(chan error, 1)
	go func() {
		_, err := manager.Exec(context.Background(), Identity{SessionID: "session-a", RunID: "run-a"}, first.ID, ExecRequest{Command: "true"})
		execDone <- err
	}()
	<-started
	timedOut := make(chan error, 1)
	go func() {
		_, err := manager.Create(context.Background(), Identity{SessionID: "session-b", RunID: "run-b"}, second.ID)
		timedOut <- err
	}()
	clock.WaitForAfter(t, 30*time.Second)
	clock.Advance(30 * time.Second)
	assertSandboxErrorCode(t, <-timedOut, CodeCapacityExhausted)

	third := mustApply(t, manager, "session-c")
	waitContext, cancelWait := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	go func() {
		_, err := manager.Create(waitContext, Identity{SessionID: "session-c", RunID: "run-c"}, third.ID)
		canceled <- err
	}()
	clock.WaitForAfter(t, 30*time.Second)
	cancelWait()
	assertSandboxErrorCode(t, <-canceled, CodeCanceled)
	close(finish)
	if err := <-execDone; err != nil {
		t.Fatal(err)
	}
	fourth := mustApply(t, manager, "session-d")
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-d", RunID: "run-d"}, fourth.ID); err != nil {
		t.Fatalf("canceled waiter left capacity blocked: %v", err)
	}
}

func TestManagerHibernateAllStopsComputeAndKeepsWorkspaces(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	first := mustApply(t, manager, "session-a")
	second := mustApply(t, manager, "session-b")
	for _, item := range []struct {
		identity Identity
		id       string
	}{{Identity{SessionID: "session-a", RunID: "run-a"}, first.ID}, {Identity{SessionID: "session-b", RunID: "run-b"}, second.ID}} {
		if _, err := manager.Create(context.Background(), item.identity, item.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.HibernateAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.runningCount() != 0 || engine.volumeCount() != 2 {
		t.Fatalf("shutdown state: running=%d volumes=%d", engine.runningCount(), engine.volumeCount())
	}
}

func TestManagerNeverRemovesContainerWhoseLabelsDoNotMatchPersistedReference(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Release(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	engine.corruptOnlyContainerLabels(map[string]string{"unrelated": "true"})
	if _, err := manager.Create(context.Background(), Identity{SessionID: "session-a", RunID: "run-b"}, application.ID); errorCode(err) != CodeInternal {
		t.Fatalf("unexpected error for deterministic-name ownership collision: %v", err)
	}
	if engine.containerCount() != 1 {
		t.Fatalf("unrelated container was removed; count=%d", engine.containerCount())
	}
}

func TestManagerStatusAndDestroyAreRaceSafe(t *testing.T) {
	manager, _, _ := newTestManager(t, testConfig())
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}
	if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsSeen := make(chan error, 32)
	var wait sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := manager.Status(context.Background(), identity, application.ID)
			if err != nil && errorCode(err) != CodeApplicationAbsent {
				errorsSeen <- err
			}
		}()
	}
	close(start)
	if err := manager.Destroy(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("unexpected concurrent status error: %v", err)
	}
}

func TestNewManagerRejectsCorruptPersistedResourceNames(t *testing.T) {
	cfg := testConfig()
	store := newMemoryStore()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	if err := store.Put(Application{ID: "app_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", SessionID: "session-a", VolumeName: "unrelated-volume", State: StateApplied, CreatedAt: now, HardExpiresAt: now.Add(time.Hour), IdleTTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(cfg, newFakeEngine(), store, newFakeClock(now)); err == nil {
		t.Fatal("corrupt persisted volume name was accepted")
	}
}

func TestNewManagerRejectsPersistedLifetimeAboveGlobalMaximum(t *testing.T) {
	cfg := testConfig()
	store := newMemoryStore()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	applicationID := "app_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := store.Put(Application{
		ID:                 applicationID,
		SessionID:          "session-a",
		VolumeName:         volumeName(cfg.Namespace, applicationID),
		State:              StateApplied,
		CreatedAt:          now,
		HardExpiresAt:      now.Add(5*time.Hour + time.Nanosecond),
		IdleTTL:            time.Minute,
		WorkspacePreserved: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(cfg, newFakeEngine(), store, newFakeClock(now)); err == nil {
		t.Fatal("persisted application above the global five-hour lifetime was accepted")
	}
}

func TestBoltStoreRoundTripsApplications(t *testing.T) {
	statePath := t.TempDir() + "/state.db"
	store, err := OpenBoltStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	want := Application{ID: "app-1", SessionID: "session-1", VolumeName: "volume-1", State: StateApplied, CreatedAt: time.Unix(10, 0).UTC(), HardExpiresAt: time.Unix(20, 0).UTC(), IdleTTL: time.Minute, SeenRunIDs: []string{"run-1"}}
	if err := store.Put(want); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	items, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != want.ID || items[0].SeenRunIDs[0] != "run-1" {
		t.Fatalf("round trip=%+v", items)
	}
	if err := store.Delete(want.ID); err != nil {
		t.Fatal(err)
	}
	items, err = store.List()
	if err != nil || len(items) != 0 {
		t.Fatalf("after delete=%+v, err=%v", items, err)
	}
}

func mustApply(t *testing.T, manager *Manager, sessionID string) ApplicationView {
	t.Helper()
	result, err := manager.Apply(context.Background(), Identity{SessionID: sessionID, RunID: "apply-run-" + sessionID})
	if err != nil {
		t.Fatal(err)
	}
	return result.Application
}

func newTestManager(t *testing.T, cfg Config) (*Manager, *fakeEngine, *fakeClock) {
	t.Helper()
	engine := newFakeEngine()
	clock := newFakeClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	manager, err := NewManager(cfg, engine, newMemoryStore(), clock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager, engine, clock
}

func testConfig() Config {
	return Config{
		Namespace:           "test",
		ListenAddress:       "127.0.0.1:8787",
		AuthToken:           "test-controller-token-0123456789abcdef",
		StatePath:           "/tmp/test-state.db",
		DockerHost:          "unix:///var/run/docker.sock",
		Image:               "sandbox:test",
		MaxRunning:          3,
		MaxApplications:     30,
		MaxStarting:         2,
		WaitTimeout:         30 * time.Second,
		BaseIdleTTL:         time.Minute,
		HardTTL:             5 * time.Hour,
		ReaperInterval:      10 * time.Second,
		CPUs:                1,
		MemoryBytes:         2 << 30,
		PIDsLimit:           256,
		ShmSizeBytes:        64 << 20,
		TmpSizeBytes:        256 << 20,
		WorkspaceSoftLimit:  1 << 30,
		TotalWorkspaceLimit: 20 << 30,
		MinFreeDisk:         10 << 30,
		MaxOutputBytes:      64 << 10,
		CommandTimeout:      2 * time.Minute,
		MaxCommandTimeout:   10 * time.Minute,
	}
}

func assertSandboxErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var sandboxErr *Error
	if !errors.As(err, &sandboxErr) || sandboxErr.Code != code {
		t.Fatalf("error=%v, want code %s", err, code)
	}
}

type memoryStore struct {
	mu    sync.Mutex
	items map[string]Application
	putFn func(Application) error
}

func newMemoryStore() *memoryStore { return &memoryStore{items: make(map[string]Application)} }

func (store *memoryStore) List() ([]Application, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]Application, 0, len(store.items))
	for _, item := range store.items {
		result = append(result, cloneApplication(item))
	}
	return result, nil
}

func (store *memoryStore) Put(application Application) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.putFn != nil {
		if err := store.putFn(application); err != nil {
			return err
		}
	}
	store.items[application.ID] = cloneApplication(application)
	return nil
}

func (store *memoryStore) Delete(applicationID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.items, applicationID)
	return nil
}

func (*memoryStore) Close() error { return nil }

func (store *memoryStore) setPutFunc(operation func(Application) error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.putFn = operation
}

type fakeClock struct {
	mu         sync.Mutex
	now        time.Time
	waiters    []*fakeClockWaiter
	afterCalls chan time.Duration
}

type fakeClockWaiter struct {
	at      time.Time
	channel chan time.Time
	fired   bool
}

type fakeClockTimer struct {
	clock  *fakeClock
	waiter *fakeClockWaiter
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now, afterCalls: make(chan time.Duration, 128)}
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) NewTimer(delay time.Duration) Timer {
	return &fakeClockTimer{clock: clock, waiter: clock.newWaiter(delay)}
}

func (clock *fakeClock) newWaiter(delay time.Duration) *fakeClockWaiter {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	channel := make(chan time.Time, 1)
	waiter := &fakeClockWaiter{at: clock.now.Add(delay), channel: channel}
	clock.waiters = append(clock.waiters, waiter)
	select {
	case clock.afterCalls <- delay:
	default:
	}
	return waiter
}

func (timer *fakeClockTimer) Channel() <-chan time.Time { return timer.waiter.channel }

func (timer *fakeClockTimer) Stop() bool {
	timer.clock.mu.Lock()
	defer timer.clock.mu.Unlock()
	if timer.waiter.fired {
		return false
	}
	for index, waiter := range timer.clock.waiters {
		if waiter == timer.waiter {
			timer.clock.waiters = append(timer.clock.waiters[:index], timer.clock.waiters[index+1:]...)
			timer.waiter.fired = true
			return true
		}
	}
	return false
}

func (clock *fakeClock) WaitForAfter(t *testing.T, delay time.Duration) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case got := <-clock.afterCalls:
			if got == delay {
				return
			}
		case <-deadline:
			t.Fatalf("clock.After(%s) was not called", delay)
		}
	}
}

func (clock *fakeClock) activeTimerCount() int {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return len(clock.waiters)
}

func (clock *fakeClock) WaitForStableTimerCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	stableSince := time.Time{}
	for time.Now().Before(deadline) {
		if clock.activeTimerCount() == want {
			if stableSince.IsZero() {
				stableSince = time.Now()
			} else if time.Since(stableSince) >= 5*time.Millisecond {
				return
			}
		} else {
			stableSince = time.Time{}
		}
		time.Sleep(100 * time.Microsecond)
	}
	t.Fatalf("clock retained %d active timers, want stable count %d", clock.activeTimerCount(), want)
}

func (clock *fakeClock) Advance(delay time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delay)
	remaining := clock.waiters[:0]
	for _, waiter := range clock.waiters {
		if waiter.at.After(clock.now) {
			remaining = append(remaining, waiter)
			continue
		}
		waiter.fired = true
		waiter.channel <- clock.now
		close(waiter.channel)
	}
	clock.waiters = remaining
	clock.mu.Unlock()
}

func (clock *fakeClock) AdvanceWithoutFiring(delay time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delay)
	clock.mu.Unlock()
}

type fakeContainer struct {
	spec    ContainerSpec
	running bool
}

type fakeEngine struct {
	mu             sync.Mutex
	volumes        map[string]map[string]string
	containers     map[string]*fakeContainer
	nextContainer  int
	stops          int
	maximumRunning int
	startOrder     []string
	startFn        func(context.Context, string) error
	createErr      error
	createOnError  bool
	createErrorID  bool
	createHook     func()
	inspectHook    func(string, ContainerInfo)
	createVolumeFn func(context.Context, string, map[string]string) (bool, error)
	stopErr        error
	removeErr      error
	removeHook     func(string)
	readyErr       error
	cleanupErr     error
	storageErr     error
	storageUsage   StorageUsage
	storageUsageFn func(context.Context, string) (StorageUsage, error)
	listManagedFn  func(context.Context, string) (ManagedResources, error)
	execFn         func(context.Context, string, EngineExecRequest) (EngineExecResult, error)
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		volumes:      make(map[string]map[string]string),
		containers:   make(map[string]*fakeContainer),
		storageUsage: StorageUsage{FreeBytes: 1 << 50},
	}
}

func (*fakeEngine) ValidateRuntimeImage(context.Context, string) error { return nil }

func (engine *fakeEngine) CreateVolume(ctx context.Context, name string, labels map[string]string) (bool, error) {
	engine.mu.Lock()
	operation := engine.createVolumeFn
	if operation != nil {
		engine.mu.Unlock()
		return operation(ctx, name, labels)
	}
	defer engine.mu.Unlock()
	if _, exists := engine.volumes[name]; !exists {
		engine.volumes[name] = cloneLabels(labels)
		return true, nil
	}
	return false, nil
}

func (engine *fakeEngine) InspectVolume(_ context.Context, name string) (VolumeInfo, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	labels, exists := engine.volumes[name]
	return VolumeInfo{Exists: exists, Labels: cloneLabels(labels)}, nil
}

func (engine *fakeEngine) CreateContainer(_ context.Context, spec ContainerSpec) (string, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.createErr != nil && !engine.createOnError {
		return "", engine.createErr
	}
	engine.nextContainer++
	id := fmt.Sprintf("%064x", engine.nextContainer)
	engine.containers[id] = &fakeContainer{spec: spec}
	if engine.createHook != nil {
		engine.createHook()
	}
	if engine.createErr != nil {
		if engine.createErrorID {
			return id, engine.createErr
		}
		return "", engine.createErr
	}
	return id, nil
}

func (engine *fakeEngine) InspectContainer(_ context.Context, reference string) (ContainerInfo, error) {
	engine.mu.Lock()
	id := reference
	container := engine.containers[id]
	if container == nil {
		for candidateID, candidate := range engine.containers {
			if candidate.spec.Name == reference {
				id, container = candidateID, candidate
				break
			}
		}
	}
	if container == nil {
		hook := engine.inspectHook
		engine.mu.Unlock()
		if hook != nil {
			hook(reference, ContainerInfo{})
		}
		return ContainerInfo{}, nil
	}
	info := ContainerInfo{ID: id, Name: container.spec.Name, Exists: true, Running: container.running, Labels: cloneLabels(container.spec.Labels)}
	hook := engine.inspectHook
	engine.mu.Unlock()
	if hook != nil {
		hook(reference, info)
	}
	return info, nil
}

func (engine *fakeEngine) StartContainer(ctx context.Context, id string) error {
	engine.mu.Lock()
	container := engine.containers[id]
	if container == nil {
		engine.mu.Unlock()
		return errors.New("container absent")
	}
	startFn := engine.startFn
	engine.mu.Unlock()
	if startFn != nil {
		if err := startFn(ctx, id); err != nil {
			return err
		}
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	container = engine.containers[id]
	if container == nil {
		return errors.New("container absent")
	}
	if !container.running {
		engine.startOrder = append(engine.startOrder, container.spec.Labels[applicationLabel])
	}
	container.running = true
	running := 0
	for _, item := range engine.containers {
		if item.running {
			running++
		}
	}
	if running > engine.maximumRunning {
		engine.maximumRunning = running
	}
	return nil
}

func (engine *fakeEngine) StopContainer(_ context.Context, id string) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.stopErr != nil {
		return engine.stopErr
	}
	if container := engine.containers[id]; container != nil && container.running {
		container.running = false
		engine.stops++
	}
	return nil
}

func (engine *fakeEngine) RemoveContainer(_ context.Context, id string) error {
	engine.mu.Lock()
	hook := engine.removeHook
	engine.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.removeErr != nil {
		return engine.removeErr
	}
	delete(engine.containers, id)
	return nil
}

func (engine *fakeEngine) RemoveVolume(_ context.Context, name string) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	delete(engine.volumes, name)
	return nil
}

func (engine *fakeEngine) Exec(ctx context.Context, containerID string, request EngineExecRequest) (EngineExecResult, error) {
	engine.mu.Lock()
	execFn := engine.execFn
	container := engine.containers[containerID]
	readyErr := engine.readyErr
	cleanupErr := engine.cleanupErr
	engine.mu.Unlock()
	if container == nil || !container.running {
		return EngineExecResult{}, errors.New("container is not running")
	}
	if len(request.Command) >= 3 && strings.Contains(request.Command[2], "mkdir -p /workspace/.cache/go-build") {
		if readyErr != nil {
			return EngineExecResult{}, readyErr
		}
		return EngineExecResult{}, nil
	}
	if len(request.Command) >= 3 && request.Command[2] == cleanupUserProcessesScript {
		if cleanupErr != nil {
			return EngineExecResult{}, cleanupErr
		}
		return EngineExecResult{}, nil
	}
	if execFn != nil {
		return execFn(ctx, containerID, request)
	}
	return EngineExecResult{ExitCode: 0}, nil
}

func (engine *fakeEngine) ListManaged(ctx context.Context, namespace string) (ManagedResources, error) {
	engine.mu.Lock()
	if engine.listManagedFn != nil {
		operation := engine.listManagedFn
		engine.mu.Unlock()
		return operation(ctx, namespace)
	}
	defer engine.mu.Unlock()
	var result ManagedResources
	for id, container := range engine.containers {
		if container.spec.Labels[namespaceLabel] == namespace {
			result.Containers = append(result.Containers, ManagedContainer{ID: id, Name: container.spec.Name, Running: container.running, Labels: cloneLabels(container.spec.Labels)})
		}
	}
	for name, labels := range engine.volumes {
		if labels[namespaceLabel] == namespace {
			result.Volumes = append(result.Volumes, ManagedVolume{Name: name, Labels: cloneLabels(labels)})
		}
	}
	return result, nil
}

func (engine *fakeEngine) StorageUsage(ctx context.Context, namespace string) (StorageUsage, error) {
	engine.mu.Lock()
	if engine.storageErr != nil {
		err := engine.storageErr
		engine.mu.Unlock()
		return StorageUsage{}, err
	}
	if engine.storageUsageFn != nil {
		operation := engine.storageUsageFn
		engine.mu.Unlock()
		return operation(ctx, namespace)
	}
	workspace := make(map[string]int64)
	for name, labels := range engine.volumes {
		if labels[namespaceLabel] == namespace {
			workspace[name] = engine.storageUsage.WorkspaceBytes[name]
		}
	}
	result := StorageUsage{WorkspaceBytes: workspace, TotalBytes: engine.storageUsage.TotalBytes, FreeBytes: engine.storageUsage.FreeBytes}
	engine.mu.Unlock()
	return result, nil
}

func (engine *fakeEngine) volumeCount() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return len(engine.volumes)
}

func (engine *fakeEngine) containerCount() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return len(engine.containers)
}

func (engine *fakeEngine) runningCount() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	count := 0
	for _, container := range engine.containers {
		if container.running {
			count++
		}
	}
	return count
}

func (engine *fakeEngine) loseAllResources() {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.volumes = make(map[string]map[string]string)
	engine.containers = make(map[string]*fakeContainer)
}

func (engine *fakeEngine) corruptOnlyContainerLabels(labels map[string]string) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	for _, container := range engine.containers {
		container.spec.Labels = cloneLabels(labels)
		return
	}
}

func (engine *fakeEngine) corruptVolumeLabels(applicationID string, labels map[string]string) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	for name, current := range engine.volumes {
		if current[applicationLabel] == applicationID {
			engine.volumes[name] = cloneLabels(labels)
			return
		}
	}
}

func (engine *fakeEngine) setStorageUsageError(err error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.storageErr = err
}

func (engine *fakeEngine) setStorageUsage(workspaceBytes, totalBytes, freeBytes int64) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.storageUsage.TotalBytes = totalBytes
	engine.storageUsage.FreeBytes = freeBytes
	engine.storageUsage.WorkspaceBytes = make(map[string]int64, len(engine.volumes))
	for name := range engine.volumes {
		engine.storageUsage.WorkspaceBytes[name] = workspaceBytes
	}
}

func (engine *fakeEngine) setStorageUsageFunc(operation func(context.Context, string) (StorageUsage, error)) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.storageUsageFn = operation
}

func (engine *fakeEngine) setStopError(err error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.stopErr = err
}

func (engine *fakeEngine) setAmbiguousCreateError(err error, returnID bool) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.createErr = err
	engine.createOnError = err != nil
	engine.createErrorID = returnID
}

func (engine *fakeEngine) setCreateHook(operation func()) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.createHook = operation
}

func (engine *fakeEngine) setReadyError(err error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.readyErr = err
}

func (engine *fakeEngine) setRemoveContainerError(err error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.removeErr = err
}

func (engine *fakeEngine) setRemoveContainerHook(hook func(string)) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.removeHook = hook
}

func (engine *fakeEngine) setCleanupError(err error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.cleanupErr = err
}

func (engine *fakeEngine) maxObservedRunning() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.maximumRunning
}

func (engine *fakeEngine) stopCount() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.stops
}

func (engine *fakeEngine) startApplicationOrder() []string {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return append([]string(nil), engine.startOrder...)
}

func waitForWaiterCount(t *testing.T, manager *Manager, count int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		manager.mu.Lock()
		got := len(manager.waiters)
		manager.mu.Unlock()
		if got == count {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("waiter count=%d, want %d", got, count)
		case <-time.After(time.Millisecond):
		}
	}
}
