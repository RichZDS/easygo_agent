package framejob

import (
	"context"
	"time"
)

// FrameInterval 时间帧间隔。
const FrameInterval = 100 * time.Millisecond

// FrameHandler 帧任务处理函数。
type FrameHandler func(ctx context.Context) error

// FrameJob 时间帧任务定义。
type FrameJob struct {
	Name         string
	MuteDuration time.Duration
	Handler      FrameHandler
}
