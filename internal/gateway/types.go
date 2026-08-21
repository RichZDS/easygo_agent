// Package gateway exposes the transport-neutral Agent execution seam.
package gateway

import "context"

// Role identifies one stable conversation role.
type Role string

const (
	// RoleSystem identifies system instructions.
	RoleSystem Role = "system"
	// RoleUser identifies user input.
	RoleUser Role = "user"
	// RoleAssistant identifies completed assistant output.
	RoleAssistant Role = "assistant"
)

// Message is the stable text-only message accepted by the template Gateway.
type Message struct {
	Role    Role
	Content string
}

// Request contains the complete current-process conversation for one run.
type Request struct {
	Messages []Message
}

// EventKind identifies one stable outward streaming event.
type EventKind string

const (
	// EventTextDelta carries incremental assistant text.
	EventTextDelta EventKind = "text_delta"
	// EventToolStart reports a Tool invocation without its arguments.
	EventToolStart EventKind = "tool_start"
	// EventToolEnd reports Tool completion without its output.
	EventToolEnd EventKind = "tool_end"
	// EventCompleted carries the authoritative final assistant text.
	EventCompleted EventKind = "completed"
	// EventCanceled reports caller-requested cancellation.
	EventCanceled EventKind = "canceled"
	// EventFailed reports a non-cancellation runtime error.
	EventFailed EventKind = "failed"
)

// Event is one Gateway stream update.
type Event struct {
	Kind     EventKind
	Text     string
	ToolName string
	Err      error
}

// EventStream receives stable Gateway events and owns cancellation cleanup.
type EventStream interface {
	Recv() (Event, error)
	Close() error
}

// Runner starts one stateless Agent run.
type Runner interface {
	Run(context.Context, Request) (EventStream, error)
}

// Config contains Eino ReAct limits owned by the Gateway.
type Config struct {
	MaxSteps int
}
