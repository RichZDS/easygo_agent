package agentruntime

import (
	"context"
	"testing"

	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/conversation"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestTerminalObservationFollowsPersistenceEvenWithoutConsumer(t *testing.T) {
	for _, mode := range []string{"completed", "canceled", "commit_failure"} {
		t.Run(mode, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			ctx := telemetry.WithLogger(context.Background(), zap.New(core))
			memory := conversation.NewMemory()
			session, err := memory.Create(ctx, "alice")
			if err != nil {
				t.Fatal(err)
			}
			var store conversation.QueueStore = memory
			if mode == "commit_failure" {
				store = failingQueueStore{QueueStore: memory}
			}
			run := mustClaim(t, ctx, store, streamAgent([]*schema.AgenticMessage{assistantTextChunk(0, "done")}), "alice", session.ID, "private input")
			if mode != "canceled" {
				drain(run)
			}
			terminal := run.Close()
			run.Close()
			want := EventCompleted
			if mode == "canceled" {
				want = EventCanceled
			}
			if mode == "commit_failure" {
				want = EventFailed
			}
			if terminal.Kind != want {
				t.Fatal(terminal)
			}
			entries := logs.FilterMessage("agent.phase").FilterField(zap.String("event", "finished")).All()
			ends, commits := 0, 0
			for i, entry := range entries {
				f := entry.ContextMap()
				if f["phase"] == "persist" {
					commits++
				}
				if f["phase"] == "run" {
					ends++
					if i != len(entries)-1 || commits == 0 || f["status"] != string(want) {
						t.Fatalf("premature or incorrect terminal: %v", f)
					}
				}
			}
			if ends != 1 {
				t.Fatalf("terminal logs=%d", ends)
			}
			if mode == "commit_failure" && commits != 2 {
				t.Fatalf("missing failed-state retry: commits=%d", commits)
			}
		})
	}
}
