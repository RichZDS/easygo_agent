package taskmanager

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

const taskName = "timeTask"

type TimeTask struct {
	mu           sync.Mutex
	log          *zap.Logger
	lastRunTime  time.Time     // 上次执行时间
	muteDuration time.Duration // 静默时间
}

func NewTimeTask(log *zap.Logger, muteDuration time.Duration) *TimeTask {
	return &TimeTask{log: log, muteDuration: muteDuration}
}

// ShouldRun 判断是否已过静默时间，可以执行
func (t *TimeTask) ShouldRun() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Since(t.lastRunTime) >= t.muteDuration
}

func (t *TimeTask) Run(ctx context.Context) {
	t.mu.Lock()
	t.lastRunTime = time.Now()
	t.mu.Unlock()

	t.log.Info("register time task", zap.String("taskName", taskName))
	// 打印当前时间
	t.log.Info("current time", zap.String("time", time.Now().Format("2006-01-02 15:04:05")))
}
