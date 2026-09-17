package deepagent

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// The common path must not repeatedly serialize an unchanged conversation.
func BenchmarkFitWithinBudget(b *testing.B) {
	messages := make([]*schema.AgenticMessage, 40)
	for i := range messages {
		messages[i] = schema.UserAgenticMessage(strings.Repeat("context ", 40))
	}
	req := FitRequest{Messages: messages, Limit: 24000}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		result, err := Fit(req)
		if err != nil || !result.Fitted {
			b.Fatalf("fit: %v", err)
		}
	}
}
