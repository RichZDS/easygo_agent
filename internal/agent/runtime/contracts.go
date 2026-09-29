package agentruntime

import (
	"errors"

	"easygo-agent/internal/clientapi"
)

var (
	// ErrEmptyInput 表示一次运行没有可发送的用户文本。
	ErrEmptyInput = errors.New("agent input cannot be empty")
	// ErrRunInProgress 表示会话中已有一次尚未结束的运行。
	ErrRunInProgress = errors.New("agent run already in progress")
	// ErrAgentUnavailable 表示会话没有可调用的 Agent。
	ErrAgentUnavailable = errors.New("agent is unavailable")
	ErrStoreUnavailable = errors.New("conversation store is unavailable")
	ErrQueueClosed      = clientapi.ErrQueueClosed
	// ErrRunFinished 表示调用方在终态事件之后再次读取运行。
	ErrRunFinished = errors.New("agent run already finished")
)

// EventKind 标识 TUI 等调用方需要处理的一种运行事件。
type EventKind = clientapi.EventKind

const (
	EventTextDelta      = clientapi.EventTextDelta
	EventReasoningDelta = clientapi.EventReasoningDelta
	EventToolStarted    = clientapi.EventToolStarted
	EventToolFinished   = clientapi.EventToolFinished
	EventCompleted      = clientapi.EventCompleted
	EventCanceled       = clientapi.EventCanceled
	EventFailed         = clientapi.EventFailed
	EventQueued         = clientapi.EventQueued
	EventRunning        = clientapi.EventRunning
	EventCompressing    = clientapi.EventCompressing
	EventCompressed     = clientapi.EventCompressed
)

// Event 是 Agent 运行投影出的语义事件。终态事件的 Text 是本次运行的完整助手文本。
type Event = clientapi.Event

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
