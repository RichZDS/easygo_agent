// Package gateway 对外暴露与传输无关的 Agent 执行缝。
package gateway

import "context"

// Role 标识一种稳定的对话角色。
type Role string

const (
	// RoleSystem 标识系统指令。
	RoleSystem Role = "system"
	// RoleUser 标识用户输入。
	RoleUser Role = "user"
	// RoleAssistant 标识已完成的助手输出。
	RoleAssistant Role = "assistant"
)

// Message 是模板 Gateway 接受的稳定纯文本消息。
type Message struct {
	Role    Role
	Content string
}

// Request 包含一次运行所需的当前进程完整对话。
type Request struct {
	Messages []Message
}

// EventKind 标识一种稳定的对外流式事件。
type EventKind string

const (
	// EventTextDelta 携带增量助手文本。
	EventTextDelta EventKind = "text_delta"
	// EventToolStart 报告 Tool 调用开始，不包含参数。
	EventToolStart EventKind = "tool_start"
	// EventToolEnd 报告 Tool 完成，不包含输出。
	EventToolEnd EventKind = "tool_end"
	// EventCompleted 携带权威的最终助手文本。
	EventCompleted EventKind = "completed"
	// EventCanceled 报告调用方请求的取消。
	EventCanceled EventKind = "canceled"
	// EventFailed 报告非取消类运行时错误。
	EventFailed EventKind = "failed"
)

// Event 是一条 Gateway 流更新。
type Event struct {
	Kind     EventKind
	Text     string
	ToolName string
	Err      error
}

// EventStream 接收稳定 Gateway 事件，并负责取消时的清理。
type EventStream interface {
	Recv() (Event, error)
	Close() error
}

// Runner 启动一次无状态 Agent 运行。
type Runner interface {
	Run(context.Context, Request) (EventStream, error)
}

// Config 包含 Gateway 持有的 Eino ReAct 限制。
type Config struct {
	MaxSteps int
}
