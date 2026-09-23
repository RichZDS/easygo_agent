package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type failingPhaseMemory struct {
	*conversation.Memory
}

func (failingPhaseMemory) AppendRunPhase(context.Context, conversation.RunPhase) error {
	return errors.New("phase store unavailable")
}

func TestRunCompletesWhenPhasePersistFails(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx := telemetry.WithLogger(context.Background(), zap.New(core))
	memory := conversation.NewMemory()
	session, err := memory.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	run := mustClaim(t, ctx, failingPhaseMemory{Memory: memory}, streamAgent([]*schema.AgenticMessage{testutil.Text("done")}), "alice", session.ID, "private prompt body")
	last := drain(run)
	if last.Kind != EventCompleted || last.Err != nil {
		t.Fatalf("terminal=%+v", last)
	}
	finished := logs.FilterMessage("agent.phase").FilterField(zap.String("phase", "run")).FilterField(zap.String("event", "finished")).All()
	if len(finished) != 1 {
		t.Fatalf("finishes=%d", len(finished))
	}
	f := finished[0].ContextMap()
	if f["status"] != "completed" {
		t.Fatalf("status=%v", f)
	}
	failures, ok := f["phase_persist_failures"].(int64)
	if !ok || failures <= 0 {
		t.Fatalf("phase_persist_failures=%v (%T)", f["phase_persist_failures"], f["phase_persist_failures"])
	}
}

func TestStoreReaderPagesRunPhasesWithoutRuntime(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	secret := "private prompt body"
	run := mustClaim(t, ctx, store, streamAgent([]*schema.AgenticMessage{testutil.Text("done")}), "alice", session.ID, secret)
	last := drain(run)
	if last.Kind != EventCompleted || last.RunID == "" {
		t.Fatalf("terminal=%+v", last)
	}
	var reader conversation.PhaseStore = store
	page, err := reader.ListRunPhases(ctx, "alice", session.ID, last.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Phases) < 2 {
		t.Fatalf("phases=%d", len(page.Phases))
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), `"message"`) || strings.Contains(string(raw), `"prompt"`) || strings.Contains(string(raw), `"arguments"`) {
		t.Fatalf("phase payload leaked: %s", raw)
	}
	rest, err := reader.ListRunPhases(ctx, "alice", session.ID, last.RunID, page.Phases[0].Sequence)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range rest.Phases {
		if phase.Sequence <= page.Phases[0].Sequence {
			t.Fatalf("after cursor returned sequence %d", phase.Sequence)
		}
	}
	if _, err = reader.ListRunPhases(ctx, "bob", session.ID, last.RunID, 0); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("mismatched user: %v", err)
	}
}
