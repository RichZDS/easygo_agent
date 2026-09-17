package agentruntime

import (
	"fmt"
	"testing"
)

// Unrelated subscriptions should not add per-token work to the running agent.
func BenchmarkPublishUnrelatedRuns(b *testing.B) {
	for _, count := range []int{1, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			manager := &queueManager{subscribers: make(map[string]map[*runSubscription]struct{})}
			for i := range count {
				manager.subscribers[fmt.Sprint(i)] = nil
			}
			event := Event{RunID: "unobserved", Kind: EventTextDelta, Text: "token"}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				manager.publish(event)
			}
		})
	}
}
