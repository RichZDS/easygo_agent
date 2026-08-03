package chat

import (
	"testing"

	"easygo-agent/internal/model"
)

// TestCompactHistory_Passthrough 验证未超限时完整透传历史，避免清空消息导致 ChatModel 空 content。
func TestCompactHistory_Passthrough(t *testing.T) {
	history := []model.ChatMessage{
		{ID: 1, Seq: 1, Role: "user", Text: "hello"},
		{ID: 2, Seq: 2, Role: "assistant", Text: "hi"},
	}
	got, err := CompactHistory(history, 40)
	if err != nil {
		t.Fatalf("CompactHistory returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Text != "hello" || got[1].Text != "hi" {
		t.Fatalf("unexpected messages: %+v", got)
	}
}

// TestCompactHistory_TrimOldest 验证超限时保留最近 N 条。
func TestCompactHistory_TrimOldest(t *testing.T) {
	history := []model.ChatMessage{
		{ID: 1, Seq: 1, Text: "a"},
		{ID: 2, Seq: 2, Text: "b"},
		{ID: 3, Seq: 3, Text: "c"},
	}
	got, err := CompactHistory(history, 2)
	if err != nil {
		t.Fatalf("CompactHistory returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Text != "b" || got[1].Text != "c" {
		t.Fatalf("expected newest two messages, got %+v", got)
	}
}
