package cronjob

import (
	"context"
)

// Logger 供 Cron handler 写入可落库的运行日志。
type Logger interface {
	Info(msg string)
	Warn(msg string)
	Error(msg string)
}

// CronHandler cron 任务处理函数。
type CronHandler func(ctx context.Context, log Logger) error

// CronJob cron 任务定义。
type CronJob struct {
	Name          string
	Spec          string // 标准 6 段 cron（含秒），如 "0 */5 * * * *"
	AllowParallel bool   // true=可重叠；false=进行中则 skip
	Handler       CronHandler
}
