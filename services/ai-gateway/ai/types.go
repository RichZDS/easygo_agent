// Package ai is EasyGo's provider-neutral model protocol. It has no agent,
// storage, CLI, or third-party framework dependencies.
package ai

import (
	"context"
	"encoding/json"
)

type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	URL       string          `json:"url,omitempty"`
	MediaType string          `json:"media_type,omitempty"`
	Data      string          `json:"data,omitempty"`
	// ProviderState carries opaque continuation data (for example signed
	// reasoning) as {"protocol": "...", "value": ...}. Only the matching
	// gateway adapter interprets it; loops must preserve it unchanged.
	ProviderState json.RawMessage `json:"provider_state,omitempty"`
}

// Roles are system, user, assistant, and tool. Block types are text,
// reasoning, tool_call, tool_result, and image. Tool results reference call ID.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Request struct {
	Model           string                     `json:"model"`
	Messages        []Message                  `json:"messages"`
	Tools           []Tool                     `json:"tools,omitempty"`
	MaxOutputTokens int                        `json:"max_output_tokens,omitempty"`
	Temperature     *float64                   `json:"temperature,omitempty"`
	ToolChoice      string                     `json:"tool_choice,omitempty"`
	Parameters      map[string]json.RawMessage `json:"parameters,omitempty"`
	RequestID       string                     `json:"request_id,omitempty"`
}

// InputTokens includes cache reads/writes; cache subsets must not be charged twice.
// Known=false means the provider did not report usage, never a free request.
type Usage struct {
	Known            bool  `json:"known"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

type Cost struct {
	Known    bool    `json:"known"`
	Currency string  `json:"currency,omitempty"`
	Amount   float64 `json:"amount"`
}

type Response struct {
	ID           string  `json:"id,omitempty"`
	Model        string  `json:"model"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Usage        Usage   `json:"usage"`
	Cost         Cost    `json:"cost"`
}

// Delta events are provisional. Only a successful Complete return is authoritative.
// Type is text_delta, reasoning_delta, or tool_call_delta. Index identifies a block.
type Event struct {
	Type  string `json:"type"`
	Index int    `json:"index,omitempty"`
	Delta string `json:"delta,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
}

// Complete calls one model turn. A nil emit requests non-streaming output;
// otherwise implementations forward deltas incrementally. Callback errors cancel
// the request. A truncated stream or context cancellation MUST return an error.
type Client interface {
	Complete(context.Context, Request, func(Event) error) (Response, error)
}
