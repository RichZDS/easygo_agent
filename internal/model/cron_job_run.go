package model

import (
	"context"
	"fmt"
	"time"

	"easygo-agent/internal/platform/errorcode"

	"gorm.io/gorm"
)

const (
	CronJobRunRunning uint8 = 1
	CronJobRunSuccess uint8 = 2
	CronJobRunFailed  uint8 = 3
	CronJobRunSkipped uint8 = 4
)

// CronJobRun maps to table `cron_job_run`.
type CronJobRun struct {
	ID           uint64     `gorm:"column:id;type:bigint unsigned;primaryKey;autoIncrement" json:"id"`
	JobName      string     `gorm:"column:job_name;type:varchar(128);not null;index:idx_cron_job_run_name_started,priority:1" json:"job_name"`
	Status       uint8      `gorm:"column:status;type:tinyint;not null;index:idx_cron_job_run_status_started,priority:1" json:"status"`
	StartedAt    time.Time  `gorm:"column:started_at;not null;index:idx_cron_job_run_name_started,priority:2;index:idx_cron_job_run_status_started,priority:2" json:"started_at"`
	FinishedAt   *time.Time `gorm:"column:finished_at" json:"finished_at"`
	ErrorMessage *string    `gorm:"column:error_message;type:varchar(1024)" json:"error_message"`
	CreatedAt    time.Time  `gorm:"column:created_at;not null;autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;not null;autoUpdateTime" json:"updated_at"`
}

func (CronJobRun) TableName() string { return "cron_job_run" }

// CreateCronJobRun 创建一次 cron 运行记录。
func CreateCronJobRun(ctx context.Context, db *gorm.DB, run *CronJobRun) error {
	if err := db.WithContext(ctx).Create(run).Error; err != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("create cron job run: %w", err))
	}
	return nil
}

// FinalizeCronJobRun 将 running 记录更新为终态。
func FinalizeCronJobRun(ctx context.Context, db *gorm.DB, runID uint64, status uint8, errMsg string) error {
	now := time.Now()
	updates := map[string]any{
		"status":      status,
		"finished_at": now,
	}
	if errMsg != "" {
		updates["error_message"] = errMsg
	}
	result := db.WithContext(ctx).Model(&CronJobRun{}).Where("id = ?", runID).Updates(updates)
	if result.Error != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("finalize cron job run: %w", result.Error))
	}
	return nil
}
