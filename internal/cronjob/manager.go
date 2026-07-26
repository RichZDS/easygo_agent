package cronjob

import (
	"context"
	"fmt"
	"sync"

	"easygo-agent/internal/platform/logger"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Manager 管理 cron 任务。
type Manager struct {
	mu      sync.Mutex
	store   runStore
	cron    *cron.Cron
	runtimes map[string]*jobRuntime
	started bool
}

// NewManager 创建 cron 管理器。
func NewManager(db *gorm.DB) *Manager {
	return newManagerWithStore(gormStore{db: db})
}

func newManagerWithStore(store runStore) *Manager {
	return &Manager{
		store:    store,
		cron:     cron.New(cron.WithSeconds()),
		runtimes: make(map[string]*jobRuntime),
	}
}

// Register 注册 cron 任务；同名重复返回错误。非法 Spec 在 Start 时校验。
func (m *Manager) Register(job CronJob) error {
	if job.Name == "" {
		return fmt.Errorf("cronjob: name is required")
	}
	if job.Spec == "" {
		return fmt.Errorf("cronjob %q: spec is required", job.Name)
	}
	if job.Handler == nil {
		return fmt.Errorf("cronjob %q: handler is required", job.Name)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return fmt.Errorf("cronjob: cannot register after start")
	}
	if _, exists := m.runtimes[job.Name]; exists {
		return fmt.Errorf("cronjob %q: already registered", job.Name)
	}
	m.runtimes[job.Name] = &jobRuntime{job: job}
	return nil
}

// Start 校验所有 Spec 并启动调度；非法 Spec 返回 error。
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil
	}

	for name, rt := range m.runtimes {
		rt := rt
		_, err := m.cron.AddFunc(rt.job.Spec, func() {
			m.triggerAsync(ctx, rt)
		})
		if err != nil {
			return fmt.Errorf("cronjob %q: invalid spec %q: %w", name, rt.job.Spec, err)
		}
	}

	m.cron.Start()
	m.started = true
	logger.Info("cronjob manager started", zap.Int("jobs", len(m.runtimes)))
	return nil
}

// Stop 停止调度并等待在途任务。
func (m *Manager) Stop() {
	m.mu.Lock()
	started := m.started
	c := m.cron
	m.mu.Unlock()
	if !started {
		return
	}
	stopCtx := c.Stop()
	<-stopCtx.Done()
}

func (m *Manager) triggerAsync(ctx context.Context, rt *jobRuntime) {
	go rt.trigger(ctx, m.store)
}

// TriggerForTest 同步触发（仅测试）。
func (m *Manager) TriggerForTest(ctx context.Context, name string) error {
	m.mu.Lock()
	rt, ok := m.runtimes[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("cronjob %q: not found", name)
	}
	rt.trigger(ctx, m.store)
	return nil
}
