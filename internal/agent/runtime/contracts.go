package agentruntime

import (
	"errors"

	"easygo-agent/internal/conversation"
)

var (
	// ErrEmptyInput 表示一次运行没有可发送的用户文本。
	ErrEmptyInput = errors.New("agent input cannot be empty")
	// ErrRunInProgress 表示会话中已有一次尚未结束的运行。
	ErrRunInProgress = errors.New("agent run already in progress")
	// ErrAgentUnavailable 表示会话没有可调用的 Agent。
	ErrAgentUnavailable = errors.New("agent is unavailable")
	ErrStoreUnavailable = errors.New("conversation store is unavailable")
	ErrQueueClosed      = errors.New("queue manager is closed")
	// ErrRunFinished 表示调用方在终态事件之后再次读取运行。
	ErrRunFinished = errors.New("agent run already finished")
)

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
	RunID    string                 `json:"run_id,omitempty"`
	Status   conversation.RunStatus `json:"status,omitempty"`
	Position int                    `json:"position,omitempty"`
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

// Run 是一条正在进行的 Agent 运行。
// Next 串行交付事件，最后交付一次终态；Cancel 和 Close 可与 Next 并发调用。
// 取消会自动完成持久化与资源释放，无需调用方继续消费事件。
type Run interface {
	// Next 阻塞直到下一条语义事件可用。调用方应循环读取直到收到终态事件。
	Next() Event
	// Cancel 非阻塞地请求取消。终态仍可由 Next 读取。
	Cancel()
	// Close 取消尚未结束的运行，等待持久化与资源释放，并返回最终结果。
	// 可重复调用；不会消费 Next 的事件，已完成的结果不会被改为 canceled。
	Close() Event
}
