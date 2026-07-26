package model

import (
	"context"
	"fmt"
	"time"

	"easygo-agent/internal/platform/errorcode"

	"gorm.io/gorm"
)

// CronJobLog maps to table `cron_job_log`.
type CronJobLog struct {
	ID        uint64    `gorm:"column:id;type:bigint unsigned;primaryKey;autoIncrement" json:"id"`
	RunID     uint64    `gorm:"column:run_id;type:bigint unsigned;not null;index:idx_cron_job_log_run_id,priority:1" json:"run_id"`
	Level     string    `gorm:"column:level;type:varchar(16);not null" json:"level"`
	Message   string    `gorm:"column:message;type:text;not null" json:"message"`
	CreatedAt time.Time `gorm:"column:created_at;not null;autoCreateTime" json:"created_at"`
}

func (CronJobLog) TableName() string { return "cron_job_log" }

// CreateCronJobLog 追加一行运行日志。
func CreateCronJobLog(ctx context.Context, db *gorm.DB, row *CronJobLog) error {
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("create cron job log: %w", err))
	}
	return nil
}

// ListCronJobLogsByRunID 按 id 升序返回某次 run 的全部日志。
func ListCronJobLogsByRunID(ctx context.Context, db *gorm.DB, runID uint64) ([]CronJobLog, error) {
	var rows []CronJobLog
	if err := db.WithContext(ctx).Where("run_id = ?", runID).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("list cron job logs: %w", err))
	}
	return rows, nil
}
