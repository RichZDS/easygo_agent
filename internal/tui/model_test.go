package tui

import (
	"strings"
	"testing"

	agentruntime "easygo-agent/internal/agent/runtime"
)

// TestFormatToolStartedIncludesDetails 验证 TUI 展示 tool 调用的 name、call_id 与 arguments。
func TestFormatToolStartedIncludesDetails(t *testing.T) {
	got := formatToolStarted(agentruntime.Event{
		Tool:      "calculator",
		CallID:    "c1",
		Arguments: `{"operation":"multiply"}`,
	})
	for _, want := range []string{"tool started: calculator", "call_id: c1", `arguments: {"operation":"multiply"}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatToolStarted missing %q in %q", want, got)
		}
	}
}

// TestFormatToolFinishedIncludesDetails 验证 TUI 展示 tool 结果的 name、call_id 与 result。
func TestFormatToolFinishedIncludesDetails(t *testing.T) {
	got := formatToolFinished(agentruntime.Event{
		Tool:   "calculator",
		CallID: "c1",
		Result: `{"result":42}`,
	})
	for _, want := range []string{"tool completed: calculator", "call_id: c1", `result: {"result":42}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatToolFinished missing %q in %q", want, got)
		}
	}
}

// TestTranscriptIncludesStreamingReasoning 验证进行中的 reasoning 会出现在 transcript。
func TestTranscriptIncludesStreamingReasoning(t *testing.T) {
	model := New(nil)
	model.reasoning = "The user wants a calculation"
	got := model.transcript()
	if !strings.Contains(got, "reasoning: The user wants a calculation") {
		t.Fatalf("transcript missing reasoning: %q", got)
	}
}
