// Package clientapi holds the declarations shared by the terminal UI
// (internal/tui), its platform adapter (internal/remotetui) and the legacy
// local app: the run queue seam, run records and run events.
//
// It imports no other easygo-agent package, so the personal CLI
// (cmd/easygo-remote) keeps building when the legacy local app is removed.
// The legacy agentruntime and conversation packages re-export the queue, run
// and event declarations as aliases; the legacy app adapts its task store to
// TaskLister.
package clientapi

import (
	"context"
	"errors"
	"time"
)

// ErrQueueClosed reports a call on a queue manager after Close.
var ErrQueueClosed = errors.New("queue manager is closed")

// RunStatus is the durable lifecycle state of a submitted user request.
type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

// RunRecord is the queue-facing representation of one submitted request.
// Position is computed for queued records and is zero for other states.
type RunRecord struct {
	Source          string     `json:"source,omitempty"`
	NotificationID  string     `json:"notification_id,omitempty"`
	ID              string     `json:"run_id"`
	SessionID       string     `json:"session_id"`
	Username        string     `json:"-"`
	Input           string     `json:"input"`
	Status          RunStatus  `json:"status"`
	Position        int        `json:"position"`
	IdempotencyKey  string     `json:"-"`
	CancelRequested bool       `json:"cancel_requested,omitempty"`
	ResultText      string     `json:"result_text,omitempty"`
	Error           string     `json:"error,omitempty"`
	TurnID          int64      `json:"turn_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	WorkerID        string     `json:"-"`
	LeaseExpiresAt  time.Time  `json:"-"`
	ClaimToken      string     `json:"-"`
}

type NotificationReader interface {
	NotificationRuns(context.Context, string, string, int64) ([]RunRecord, error)
}

// EventKind 标识 TUI 等调用方需要处理的一种运行事件。
type EventKind string

const (
	// EventTextDelta 表示助手文本增量。
	EventTextDelta EventKind = "text_delta"
	// EventReasoningDelta 表示 reasoning 文本增量。
	EventReasoningDelta EventKind = "reasoning_delta"
	// EventToolStarted 表示工具调用开始，携带完整 name、call_id 与 arguments。
	EventToolStarted EventKind = "tool_started"
	// EventToolFinished 表示工具调用结束，携带完整 name、call_id 与 result。
	EventToolFinished EventKind = "tool_finished"
	// EventCompleted 表示运行成功结束。
	EventCompleted EventKind = "completed"
	// EventCanceled 表示运行被取消。
	EventCanceled EventKind = "canceled"
	// EventFailed 表示运行因错误失败。
	EventFailed EventKind = "failed"
	// EventQueued 表示请求已持久化并等待 worker。
	EventQueued EventKind = "queued"
	// EventRunning 表示请求已被 worker claim。
	EventRunning     EventKind = "running"
	EventCompressing EventKind = "compressing"
	EventCompressed  EventKind = "compressed"
)

// Event 是 Agent 运行投影出的语义事件。终态事件的 Text 是本次运行的完整助手文本。
type Event struct {
	// Queue metadata is populated for runs executed by QueueManager.
	RunID    string    `json:"run_id,omitempty"`
	Status   RunStatus `json:"status,omitempty"`
	Position int       `json:"position,omitempty"`
	// Kind 标识事件类型。
	Kind EventKind
	// Text 是文本或 reasoning 增量；终态事件则为本次运行的完整助手文本。
	Text string
	// Tool 是工具名称，仅工具生命周期事件填充。
	Tool string
	// CallID 是工具调用 ID。
	CallID string
	// Arguments 是工具调用的完整参数。
	Arguments string
	// Result 是工具返回的完整结果。
	Result string
	// Err 仅 Failed 终态携带错误。
	Err error
}

// IsTerminal reports whether this event ends a run.
func (event Event) IsTerminal() bool {
	return event.Kind == EventCompleted || event.Kind == EventCanceled || event.Kind == EventFailed
}

// QueueManager is the shared seam used by CLI, TUI and HTTP modes.
type QueueManager interface {
	Submit(context.Context, string, string, string, string) (RunRecord, RunHandle, error)
	Get(context.Context, string, string, string) (RunRecord, error)
	List(context.Context, string, string, int) ([]RunRecord, error)
	Cancel(context.Context, string, string, string) (RunRecord, error)
	Subscribe(context.Context, string, string, string) (Subscription, error)
	Close() error
}

// RunHandle identifies an accepted request. Closing a handle only releases
// the caller's handle; it never cancels the durable run.
type RunHandle interface {
	Run() RunRecord
	Cancel()
	Close()
}

// Subscription observes one run. Publication is decoupled from the worker so
// a slow observer never blocks execution or causes an event to be dropped.
type Subscription interface {
	Events() <-chan Event
	Close()
}

// TaskSummary is the part of a legacy background task that the terminal
// shows. The platform CLI does not show background tasks.
type TaskSummary struct {
	ID       string
	Version  int
	Status   string
	Goal     string
	Progress string
	// LastSeq is the sequence number of the newest task event, 0 without events.
	LastSeq int64
}

// TaskLister lists the background tasks owned by one user session.
type TaskLister interface {
	ListTasks(ctx context.Context, user, session string) ([]TaskSummary, error)
}
