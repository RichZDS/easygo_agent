package framejob

import (
	"context"
	"fmt"
	"sync"
	"time"

	"easygo-agent/internal/platform/logger"

	"go.uber.org/zap"
)

type jobState struct {
	job         FrameJob
	mu          sync.Mutex
	lastRunTime time.Time
}

// Manager 管理时间帧任务。
type Manager struct {
	mu       sync.Mutex
	jobs     map[string]*jobState
	interval time.Duration
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	started  bool
}

// NewManager 创建帧任务管理器。
func NewManager() *Manager {
	return &Manager{
		jobs:     make(map[string]*jobState),
		interval: FrameInterval,
	}
}

// Register 注册帧任务；同名重复返回错误。
func (m *Manager) Register(job FrameJob) error {
	if job.Name == "" {
		return fmt.Errorf("framejob: name is required")
	}
	if job.Handler == nil {
		return fmt.Errorf("framejob %q: handler is required", job.Name)
	}
	if job.MuteDuration < 0 {
		return fmt.Errorf("framejob %q: mute duration must be >= 0", job.Name)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return fmt.Errorf("framejob: cannot register after start")
	}
	if _, exists := m.jobs[job.Name]; exists {
		return fmt.Errorf("framejob %q: already registered", job.Name)
	}
	m.jobs[job.Name] = &jobState{job: job}
	return nil
}

// Start 启动帧循环；可重复调用（第二次 no-op）。
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.started = true
	jobs := make([]*jobState, 0, len(m.jobs))
	for _, st := range m.jobs {
		jobs = append(jobs, st)
	}
	interval := m.interval
	m.mu.Unlock()

	m.wg.Add(1)
	go m.loop(runCtx, jobs, interval)
}

// Stop 停止帧循环并等待在途任务结束。
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

func (m *Manager) loop(ctx context.Context, jobs []*jobState, interval time.Duration) {
	defer m.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.tick(ctx, jobs, now)
		}
	}
}

func (m *Manager) tick(ctx context.Context, jobs []*jobState, now time.Time) {
	for _, st := range jobs {
		if !st.mu.TryLock() {
			continue
		}
		if now.Before(st.lastRunTime.Add(st.job.MuteDuration)) {
			st.mu.Unlock()
			continue
		}
		st.lastRunTime = now
		m.wg.Add(1)
		go func(st *jobState) {
			defer m.wg.Done()
			defer st.mu.Unlock()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("framejob panic",
						zap.String("job", st.job.Name),
						zap.Any("panic", r),
					)
				}
			}()
			if err := st.job.Handler(ctx); err != nil {
				logger.Error("framejob failed",
					zap.String("job", st.job.Name),
					zap.Error(err),
				)
			}
		}(st)
	}
}
