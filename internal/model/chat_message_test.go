package model

import (
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestChatMessageEinoRoundTrip(t *testing.T) {
	t.Parallel()

	want := &schema.Message{
		Role:    schema.Assistant,
		Content: "answer",
		Name:    "easygo-chat",
		ToolCalls: []schema.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "lookup",
				Arguments: `{"id":1}`,
			},
		}},
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "tool_calls",
			Usage: &schema.TokenUsage{
				PromptTokens:     10,
				CompletionTokens: 3,
				TotalTokens:      13,
			},
		},
		ReasoningContent: "reasoning",
		Extra:            map[string]any{"provider_request_id": "req-1"},
	}

	var stored ChatMessage
	stored.ApplyEinoMessage(want)
	got := stored.ToEinoMessage()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Eino message round trip mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestApplyEinoMessageIgnoresNil(t *testing.T) {
	t.Parallel()

	stored := ChatMessage{Role: schema.User, Content: "unchanged"}
	stored.ApplyEinoMessage(nil)

	if stored.Role != schema.User || stored.Content != "unchanged" {
		t.Fatalf("nil Eino message changed stored message: %#v", stored)
	}
}
