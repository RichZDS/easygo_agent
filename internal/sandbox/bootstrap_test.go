package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestBootstrapManagerRemovesManagedContainersAfterStateDecodeFailureAndPreservesVolumes(t *testing.T) {
	cfg := testConfig()
	cfg.StatePath = t.TempDir() + "/state.db"
	store, err := OpenBoltStore(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(applicationBucket).Put([]byte("corrupt"), []byte("{"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	engine := newFakeEngine()
	seedManagedRecoveryResources(t, cfg, engine)
	manager, err := BootstrapManager(context.Background(), cfg, engine, newFakeClock(time.Now()))
	if manager != nil {
		t.Fatal("bootstrap unexpectedly returned a manager")
	}
	if err == nil || !strings.Contains(err.Error(), "decode application state") {
		t.Fatalf("bootstrap error = %v", err)
	}
	if engine.containerCount() != 0 || engine.runningCount() != 0 {
		t.Fatalf("emergency cleanup left containers=%d running=%d", engine.containerCount(), engine.runningCount())
	}
	if engine.volumeCount() != 1 {
		t.Fatalf("emergency cleanup removed recoverable workspace volume: volumes=%d", engine.volumeCount())
	}
}

func TestBootstrapManagerRemovesManagedContainersAfterRecordValidationFailure(t *testing.T) {
	cfg := testConfig()
	cfg.StatePath = t.TempDir() + "/state.db"
	store, err := OpenBoltStore(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Put(Application{
		ID:            "app_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		SessionID:     "session-a",
		VolumeName:    "wrong-volume-name",
		State:         StateApplied,
		CreatedAt:     now,
		HardExpiresAt: now.Add(time.Hour),
		IdleTTL:       time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	engine := newFakeEngine()
	seedManagedRecoveryResources(t, cfg, engine)
	manager, err := BootstrapManager(context.Background(), cfg, engine, newFakeClock(now))
	if manager != nil {
		t.Fatal("bootstrap unexpectedly returned a manager")
	}
	if err == nil || !strings.Contains(err.Error(), "workspace volume name does not match") {
		t.Fatalf("bootstrap error = %v", err)
	}
	if engine.containerCount() != 0 || engine.runningCount() != 0 || engine.volumeCount() != 1 {
		t.Fatalf("recovery resources after cleanup: containers=%d running=%d volumes=%d", engine.containerCount(), engine.runningCount(), engine.volumeCount())
	}
}

func TestBootstrapManagerDoesNotCleanupWhenBoltStoreIsLocked(t *testing.T) {
	cfg := testConfig()
	cfg.StatePath = t.TempDir() + "/state.db"
	lockedStore, err := OpenBoltStore(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer lockedStore.Close()

	engine := newFakeEngine()
	seedManagedRecoveryResources(t, cfg, engine)
	manager, err := BootstrapManager(context.Background(), cfg, engine, newFakeClock(time.Now()))
	if manager != nil {
		t.Fatal("bootstrap unexpectedly returned a manager")
	}
	if err == nil || !strings.Contains(err.Error(), "open bbolt state") {
		t.Fatalf("bootstrap error = %v", err)
	}
	if engine.containerCount() != 1 || engine.runningCount() != 1 || engine.volumeCount() != 1 {
		t.Fatalf("lock failure triggered cleanup: containers=%d running=%d volumes=%d", engine.containerCount(), engine.runningCount(), engine.volumeCount())
	}
}

func TestBootstrapManagerDoesNotCleanupWhenBoltStoreCannotOpen(t *testing.T) {
	cfg := testConfig()
	cfg.StatePath = t.TempDir()
	engine := newFakeEngine()
	seedManagedRecoveryResources(t, cfg, engine)

	manager, err := BootstrapManager(context.Background(), cfg, engine, newFakeClock(time.Now()))
	if manager != nil {
		t.Fatal("bootstrap unexpectedly returned a manager")
	}
	if err == nil || !strings.Contains(err.Error(), "open bbolt state") {
		t.Fatalf("bootstrap error = %v", err)
	}
	if engine.containerCount() != 1 || engine.runningCount() != 1 || engine.volumeCount() != 1 {
		t.Fatalf("open failure triggered cleanup: containers=%d running=%d volumes=%d", engine.containerCount(), engine.runningCount(), engine.volumeCount())
	}
}

func TestBootstrapManagerRestoresUnexpiredApplication(t *testing.T) {
	cfg := testConfig()
	cfg.StatePath = t.TempDir() + "/state.db"
	engine := newFakeEngine()
	clock := newFakeClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	manager, err := BootstrapManager(context.Background(), cfg, engine, clock)
	if err != nil {
		t.Fatal(err)
	}
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-1"}
	if _, err := manager.Create(context.Background(), identity, application.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	restored, err := BootstrapManager(context.Background(), cfg, engine, clock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	status, err := restored.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.ID != application.ID || status.Application.State == StateDestroyed {
		t.Fatalf("restored application=%+v", status.Application)
	}
	if engine.volumeCount() != 1 {
		t.Fatalf("restart dropped the workspace volume: volumes=%d", engine.volumeCount())
	}
}

func TestBootstrapManagerKeepsRestoreAndEmergencyCleanupErrors(t *testing.T) {
	cfg := testConfig()
	cfg.StatePath = t.TempDir() + "/state.db"
	store, err := OpenBoltStore(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(applicationBucket).Put([]byte("corrupt"), []byte("not-json"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	engine := newFakeEngine()
	seedManagedRecoveryResources(t, cfg, engine)
	cleanupFailure := errors.New("injected emergency removal failure")
	engine.setRemoveContainerError(cleanupFailure)
	manager, err := BootstrapManager(context.Background(), cfg, engine, newFakeClock(time.Now()))
	if manager != nil {
		t.Fatal("bootstrap unexpectedly returned a manager")
	}
	if err == nil || !strings.Contains(err.Error(), "decode application state") || !errors.Is(err, cleanupFailure) {
		t.Fatalf("bootstrap did not retain both failures: %v", err)
	}
	if engine.containerCount() != 1 || engine.volumeCount() != 1 {
		t.Fatalf("failed emergency removal changed resources: containers=%d volumes=%d", engine.containerCount(), engine.volumeCount())
	}
}

func seedManagedRecoveryResources(t *testing.T, cfg Config, engine *fakeEngine) {
	t.Helper()
	applicationID := "app_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	labels := map[string]string{
		LabelManaged:       "true",
		LabelNamespace:     cfg.Namespace,
		LabelApplicationID: applicationID,
		LabelResource:      "volume",
		LabelHardExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	}
	volume := volumeName(cfg.Namespace, applicationID)
	if _, err := engine.CreateVolume(context.Background(), volume, labels); err != nil {
		t.Fatal(err)
	}
	labels[LabelResource] = "container"
	containerID, err := engine.CreateContainer(context.Background(), ContainerSpec{
		Name:       containerName(cfg.Namespace, applicationID),
		VolumeName: volume,
		Labels:     labels,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.StartContainer(context.Background(), containerID); err != nil {
		t.Fatal(err)
	}
}
