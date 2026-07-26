package framejob

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestManager_MuteDurationSkips(t *testing.T) {
	var runs atomic.Int32
	m := NewManager()
	m.interval = 20 * time.Millisecond

	if err := m.Register(FrameJob{
		Name:         "muted",
		MuteDuration: 200 * time.Millisecond,
		Handler: func(ctx context.Context) error {
			runs.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	time.Sleep(120 * time.Millisecond)
	cancel()
	m.Stop()

	got := runs.Load()
	if got != 1 {
		t.Fatalf("expected 1 run within mute window, got %d", got)
	}
}

func TestManager_LockSkipsOverlap(t *testing.T) {
	var started atomic.Int32
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	block := make(chan struct{})

	m := NewManager()
	m.interval = 15 * time.Millisecond

	if err := m.Register(FrameJob{
		Name:         "locked",
		MuteDuration: 0,
		Handler: func(ctx context.Context) error {
			started.Add(1)
			cur := concurrent.Add(1)
			for {
				old := maxConcurrent.Load()
				if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
					break
				}
			}
			defer concurrent.Add(-1)
			select {
			case <-block:
			case <-ctx.Done():
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	deadline := time.Now().Add(500 * time.Millisecond)
	for started.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if started.Load() < 1 {
		t.Fatal("handler never started")
	}

	// Hold the lock across several frames, then release and wait for a second start.
	time.Sleep(80 * time.Millisecond)
	if maxConcurrent.Load() != 1 {
		t.Fatalf("expected max concurrent 1 while blocked, got %d", maxConcurrent.Load())
	}
	close(block)

	deadline = time.Now().Add(500 * time.Millisecond)
	for started.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	m.Stop()

	if maxConcurrent.Load() != 1 {
		t.Fatalf("expected max concurrent 1, got %d", maxConcurrent.Load())
	}
	if started.Load() < 2 {
		t.Fatalf("expected at least 2 starts after unlock, got %d", started.Load())
	}
}
