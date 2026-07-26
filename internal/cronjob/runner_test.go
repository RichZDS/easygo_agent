package cronjob

import (
	"context"
	"sync"
	"testing"
	"time"

	"easygo-agent/internal/model"
)

type memStore struct {
	mu   sync.Mutex
	next uint64
	runs []*model.CronJobRun
	logs []*model.CronJobLog
}

func (s *memStore) CreateRun(ctx context.Context, run *model.CronJobRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	cp := *run
	cp.ID = s.next
	run.ID = s.next
	s.runs = append(s.runs, &cp)
	return nil
}

func (s *memStore) FinalizeRun(ctx context.Context, runID uint64, status uint8, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, r := range s.runs {
		if r.ID == runID {
			r.Status = status
			r.FinishedAt = &now
			if errMsg != "" {
				msg := errMsg
				r.ErrorMessage = &msg
			}
			return nil
		}
	}
	return nil
}

func (s *memStore) AppendLog(ctx context.Context, row *model.CronJobLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	cp := *row
	cp.ID = s.next
	s.logs = append(s.logs, &cp)
	return nil
}

func (s *memStore) snapshot() (runs []*model.CronJobRun, logs []*model.CronJobLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		cp := *r
		runs = append(runs, &cp)
	}
	for _, l := range s.logs {
		cp := *l
		logs = append(logs, &cp)
	}
	return runs, logs
}

func TestSerialSkipWhenRunning(t *testing.T) {
	store := &memStore{}
	m := newManagerWithStore(store)

	started := make(chan struct{})
	release := make(chan struct{})
	if err := m.Register(CronJob{
		Name:          "serial",
		Spec:          "0 0 * * * *",
		AllowParallel: false,
		Handler: func(ctx context.Context, log Logger) error {
			close(started)
			<-release
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = m.TriggerForTest(ctx, "serial")
	}()

	<-started
	_ = m.TriggerForTest(ctx, "serial")
	close(release)
	<-done

	runs, _ := store.snapshot()
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	var sawSuccess, sawSkipped bool
	for _, r := range runs {
		switch r.Status {
		case model.CronJobRunSuccess:
			sawSuccess = true
		case model.CronJobRunSkipped:
			sawSkipped = true
		}
	}
	if !sawSuccess || !sawSkipped {
		t.Fatalf("expected success+skipped, got statuses %#v", statuses(runs))
	}
}

func TestParallelAllowsOverlap(t *testing.T) {
	store := &memStore{}
	m := newManagerWithStore(store)

	var mu sync.Mutex
	active := 0
	maxActive := 0
	gate := make(chan struct{})

	if err := m.Register(CronJob{
		Name:          "parallel",
		Spec:          "0 0 * * * *",
		AllowParallel: true,
		Handler: func(ctx context.Context, log Logger) error {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			<-gate
			mu.Lock()
			active--
			mu.Unlock()
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = m.TriggerForTest(ctx, "parallel") }()
	go func() { defer wg.Done(); _ = m.TriggerForTest(ctx, "parallel") }()

	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		n := active
		mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("handlers did not overlap, active=%d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	wg.Wait()

	if maxActive < 2 {
		t.Fatalf("expected overlap maxActive>=2, got %d", maxActive)
	}
	runs, _ := store.snapshot()
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	for _, r := range runs {
		if r.Status != model.CronJobRunSuccess {
			t.Fatalf("expected success, got %d", r.Status)
		}
	}
}

func TestHandlerLogsPersisted(t *testing.T) {
	store := &memStore{}
	m := newManagerWithStore(store)
	if err := m.Register(CronJob{
		Name: "logger",
		Spec: "0 0 * * * *",
		Handler: func(ctx context.Context, log Logger) error {
			log.Info("hello")
			log.Warn("careful")
			log.Error("boom")
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.TriggerForTest(context.Background(), "logger"); err != nil {
		t.Fatal(err)
	}
	runs, logs := store.snapshot()
	if len(runs) != 1 || runs[0].Status != model.CronJobRunSuccess {
		t.Fatalf("unexpected runs: %#v", runs)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(logs))
	}
	if logs[0].Level != "info" || logs[0].Message != "hello" {
		t.Fatalf("unexpected first log: %#v", logs[0])
	}
}

func TestStartInvalidSpec(t *testing.T) {
	m := newManagerWithStore(&memStore{})
	if err := m.Register(CronJob{
		Name:    "bad",
		Spec:    "not-a-cron",
		Handler: func(ctx context.Context, log Logger) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected invalid spec error")
	}
}

func statuses(runs []*model.CronJobRun) []uint8 {
	out := make([]uint8, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.Status)
	}
	return out
}
