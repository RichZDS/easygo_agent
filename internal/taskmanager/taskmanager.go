package taskmanager

import (
	"context"
	"time"

	"go.uber.org/zap"
)

type TaskManager struct {
	log   *zap.Logger
	tasks []*TimeTask // 已注册的任务列表
}

func NewTaskManager(log *zap.Logger) *TaskManager {
	return &TaskManager{log: log}
}

const Tick = 100 * time.Millisecond // 100毫秒执行一次

// Run 运行任务循环，每 Tick 检查一次所有任务
func (t *TaskManager) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			t.executeAllTasks(ctx)
			time.Sleep(Tick)
		}
	}
}

// Register 注册一个任务
func (t *TaskManager) Register(task *TimeTask) {
	t.tasks = append(t.tasks, task)
}

// RegisterBuiltinTasks 注册所有内置任务（后续新增任务在此统一管理）
func (t *TaskManager) RegisterBuiltinTasks() {
	// 时间任务：每 10 秒执行一次
	t.Register(NewTimeTask(t.log, 10*time.Second))
	// TODO: 在此注册更多任务
}

// executeAllTasks 遍历所有任务，静默时间已过的并行执行
func (t *TaskManager) executeAllTasks(ctx context.Context) {
	for _, task := range t.tasks {
		if task.ShouldRun() {
			go task.Run(ctx) // 并行执行
		}
	}
}
