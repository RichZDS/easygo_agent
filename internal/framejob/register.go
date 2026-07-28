package framejob

import (
	"context"
	"time"

	"easygo-agent/internal/platform/logger"

	"go.uber.org/zap"
)

// RegisterBuiltinFrameJobs 集中注册内置帧任务。
func RegisterBuiltinFrameJobs(m *Manager) {
	if err := m.Register(FrameJob{
		Name:         "hourly_time_log",
		MuteDuration: time.Hour,
		Handler:      logCurrentTime,
	}); err != nil {
		logger.Error("register builtin framejob failed",
			zap.String("job", "hourly_time_log"),
			zap.Error(err),
		)
	}
}

// logCurrentTime 每小时向日志输出当前时间。
func logCurrentTime(ctx context.Context) error {
	_ = ctx
	logger.Info("hourly time tick", zap.Time("now", time.Now()))
	return nil
}
