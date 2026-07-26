package cronjob

import (
	"context"
	"fmt"
	"sync"
	"time"

	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/logger"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

type runStore interface {
	CreateRun(ctx context.Context, run *model.CronJobRun) error
	FinalizeRun(ctx context.Context, runID uint64, status uint8, errMsg string) error
	AppendLog(ctx context.Context, row *model.CronJobLog) error
}

type gormStore struct {
	db *gorm.DB
}

func (s gormStore) CreateRun(ctx context.Context, run *model.CronJobRun) error {
	return model.CreateCronJobRun(ctx, s.db, run)
}

func (s gormStore) FinalizeRun(ctx context.Context, runID uint64, status uint8, errMsg string) error {
	return model.FinalizeCronJobRun(ctx, s.db, runID, status, errMsg)
}

func (s gormStore) AppendLog(ctx context.Context, row *model.CronJobLog) error {
	return model.CreateCronJobLog(ctx, s.db, row)
}

type jobRuntime struct {
	job CronJob
	mu  sync.Mutex
}

type dbLogger struct {
	ctx   context.Context
	store runStore
	runID uint64
}

func (l *dbLogger) Info(msg string)  { l.write("info", msg) }
func (l *dbLogger) Warn(msg string)  { l.write("warn", msg) }
func (l *dbLogger) Error(msg string) { l.write("error", msg) }

func (l *dbLogger) write(level, msg string) {
	if err := l.store.AppendLog(l.ctx, &model.CronJobLog{
		RunID:   l.runID,
		Level:   level,
		Message: msg,
	}); err != nil {
		logger.Error("append cron job log failed",
			zap.Uint64("run_id", l.runID),
			zap.Error(err),
		)
	}
}

func (rt *jobRuntime) trigger(ctx context.Context, store runStore) {
	if !rt.job.AllowParallel {
		if !rt.mu.TryLock() {
			rt.writeSkipped(ctx, store)
			return
		}
		defer rt.mu.Unlock()
	}

	now := time.Now()
	run := &model.CronJobRun{
		JobName:   rt.job.Name,
		Status:    model.CronJobRunRunning,
		StartedAt: now,
	}
	if err := store.CreateRun(ctx, run); err != nil {
		logger.Error("create cron job run failed",
			zap.String("job", rt.job.Name),
			zap.Error(err),
		)
		return
	}

	logWriter := &dbLogger{ctx: ctx, store: store, runID: run.ID}
	var runErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("panic: %v", r)
			}
		}()
		runErr = rt.job.Handler(ctx, logWriter)
	}()

	status := model.CronJobRunSuccess
	errMsg := ""
	if runErr != nil {
		status = model.CronJobRunFailed
		errMsg = runErr.Error()
		if len(errMsg) > 1024 {
			errMsg = errMsg[:1024]
		}
	}
	if err := store.FinalizeRun(ctx, run.ID, status, errMsg); err != nil {
		logger.Error("finalize cron job run failed",
			zap.String("job", rt.job.Name),
			zap.Uint64("run_id", run.ID),
			zap.Error(err),
		)
	}
}

func (rt *jobRuntime) writeSkipped(ctx context.Context, store runStore) {
	now := time.Now()
	run := &model.CronJobRun{
		JobName:    rt.job.Name,
		Status:     model.CronJobRunSkipped,
		StartedAt:  now,
		FinishedAt: &now,
	}
	if err := store.CreateRun(ctx, run); err != nil {
		logger.Error("create skipped cron job run failed",
			zap.String("job", rt.job.Name),
			zap.Error(err),
		)
	}
}
