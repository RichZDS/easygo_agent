package chat

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestConsumeMessageOutputProjectsRegularEinoMessage(t *testing.T) {
	t.Parallel()

	message := &schema.Message{
		Role:    schema.Assistant,
		Content: "hello",
		ToolCalls: []schema.ToolCall{{
			ID: "call-1",
			Function: schema.FunctionCall{
				Name:      "lookup",
				Arguments: `{}`,
			},
		}},
	}
	event := adk.EventFromMessage(message, nil, schema.Assistant, "")
	event.AgentName = "easygo-chat"

	var events []StreamEvent
	outputs, err := consumeMessageOutput(context.Background(), event, func(event StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("consume regular output: %v", err)
	}
	if len(outputs) != 1 || outputs[0] != message {
		t.Fatalf("unexpected persisted outputs: %#v", outputs)
	}
	if len(events) != 1 || events[0].Type != "message" {
		t.Fatalf("unexpected SSE projection: %#v", events)
	}
	payload, ok := events[0].Payload.(EinoSSEEvent)
	if !ok || payload.Content != "hello" || len(payload.ToolCalls) != 1 {
		t.Fatalf("unexpected Eino SSE payload: %#v", events[0].Payload)
	}
}

func TestConsumeMessageOutputConcatenatesNativeStream(t *testing.T) {
	t.Parallel()

	stream := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "你"},
		{Role: schema.Assistant, Content: "好"},
	})
	event := adk.EventFromMessage(nil, stream, schema.Assistant, "")
	event.AgentName = "easygo-chat"

	var eventTypes []string
	outputs, err := consumeMessageOutput(context.Background(), event, func(event StreamEvent) error {
		eventTypes = append(eventTypes, event.Type)
		return nil
	})
	if err != nil {
		t.Fatalf("consume streaming output: %v", err)
	}
	if len(outputs) != 1 || outputs[0].Content != "你好" {
		t.Fatalf("unexpected concatenated output: %#v", outputs)
	}
	if len(eventTypes) != 2 || eventTypes[0] != "stream_chunk" || eventTypes[1] != "stream_chunk" {
		t.Fatalf("unexpected streamed event types: %#v", eventTypes)
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	t.Parallel()

	if got := truncate("你好世界", 3); got != "你好世" {
		t.Fatalf("unexpected rune-safe truncation: %q", got)
	}
}

func TestMessageProjectionClassifiesEmitterFailureAsDisconnect(t *testing.T) {
	t.Parallel()

	message := &schema.Message{Role: schema.Assistant, Content: "partial"}
	event := adk.EventFromMessage(message, nil, schema.Assistant, "")
	_, err := consumeMessageOutput(context.Background(), event, func(StreamEvent) error {
		return fmt.Errorf("broken pipe")
	})
	if !errors.Is(err, errClientDisconnected) {
		t.Fatalf("expected client disconnect classification, got %v", err)
	}
}
