package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"easygo-agent/internal/agent/telemetry"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestMemorySpanPhasesPageWithoutRuntime(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	user := "phase-span-" + uuid.NewString()
	session, err := store.Create(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	secret := "private prompt body"
	run, err := store.Enqueue(ctx, user, session.ID, secret, "")
	if err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zap.InfoLevel)
	traceCtx := telemetry.WithLogger(ctx, zap.New(core))
	traceCtx = telemetry.WithPhaseSink(traceCtx, memoryPhaseSink{store: store})
	traceCtx, span := telemetry.StartRun(traceCtx, telemetry.Identity{SessionID: session.ID, RunID: run.ID}, zap.String("prompt", secret), zap.String("message", secret))
	span.Finish("completed", nil)

	if logs.FilterMessage("agent.phase").FilterField(zap.String("prompt", secret)).Len() == 0 {
		t.Fatal("local phase log dropped the prompt field")
	}
	var reader PhaseStore = store
	page, err := reader.ListRunPhases(ctx, user, session.ID, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Phases) != 2 || page.NextAfter != 2 {
		t.Fatalf("page=%+v", page)
	}
	if page.Phases[0].Event != "started" || page.Phases[0].Sequence != 1 || page.Phases[0].Status != "" || page.Phases[0].DurationMS != nil {
		t.Fatalf("started=%+v", page.Phases[0])
	}
	if page.Phases[1].Event != "finished" || page.Phases[1].Status != "completed" || page.Phases[1].DurationMS == nil || page.Phases[1].ExecutionID != page.Phases[0].ExecutionID {
		t.Fatalf("finished=%+v", page.Phases[1])
	}
	rest, err := reader.ListRunPhases(ctx, user, session.ID, run.ID, page.Phases[0].Sequence)
	if err != nil || len(rest.Phases) != 1 || rest.Phases[0].Sequence != 2 {
		t.Fatalf("after page=%+v err=%v", rest, err)
	}
	assertPhasePayload(t, page, secret)
	if _, err = reader.ListRunPhases(ctx, "other-"+user, session.ID, run.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatched user: %v", err)
	}
	if _, getErr := store.GetRun(ctx, "other-"+user, session.ID, run.ID); !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("GetRun mismatch: %v", getErr)
	}
}

type memoryPhaseSink struct{ store PhaseStore }

func (s memoryPhaseSink) PersistPhase(ctx context.Context, event telemetry.PhaseEvent) error {
	record := RunPhase{
		RunID: event.RunID, ExecutionID: event.ExecutionID, Sequence: int64(event.Sequence),
		SpanID: int64(event.SpanID), ParentSpanID: int64(event.ParentSpanID),
		Phase: event.Phase, Name: event.Name, Event: event.Event, Status: event.Status, Error: event.Error,
	}
	if event.HasDuration {
		duration := event.DurationMS
		record.DurationMS = &duration
	}
	return s.store.AppendRunPhase(ctx, record)
}

func testRunPhases(t *testing.T, store interface {
	QueueStore
	PhaseStore
}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	user := "phase-contract-" + uuid.NewString()
	session, err := store.Create(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if pg, ok := store.(*Postgres); ok {
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_, _ = pg.pool.Exec(cleanup, "DELETE FROM agent_run_phases WHERE run_id IN (SELECT id FROM agent_runs WHERE session_id IN (SELECT id FROM agent_sessions WHERE username=$1))", user)
			_, _ = pg.pool.Exec(cleanup, "DELETE FROM agent_runs WHERE session_id IN (SELECT id FROM agent_sessions WHERE username=$1)", user)
			_, _ = pg.pool.Exec(cleanup, "DELETE FROM agent_turns WHERE session_id IN (SELECT id FROM agent_sessions WHERE username=$1)", user)
			_, _ = pg.pool.Exec(cleanup, "DELETE FROM agent_sessions WHERE username=$1", user)
		})
	}
	secret := "private prompt body"
	run, err := store.Enqueue(ctx, user, session.ID, secret, "")
	if err != nil {
		t.Fatal(err)
	}
	execA, execB := uuid.NewString(), uuid.NewString()
	if execA > execB {
		execA, execB = execB, execA
	}
	duration := 3.5
	later := RunPhase{RunID: run.ID, ExecutionID: execA, Sequence: 2, SpanID: 1, Phase: "run", Name: "agent", Event: "finished", Status: "completed", DurationMS: &duration}
	earlier := RunPhase{RunID: run.ID, ExecutionID: execB, Sequence: 1, SpanID: 1, Phase: "tool", Name: "calculator", Event: "started"}
	if err = store.AppendRunPhase(ctx, earlier); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendRunPhase(ctx, later); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendRunPhase(ctx, earlier); err == nil {
		t.Fatal("duplicate phase inserted")
	}
	var reader PhaseStore = store
	page, err := reader.ListRunPhases(ctx, user, session.ID, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Phases) != 2 || page.Phases[0].ExecutionID != execA || page.Phases[0].Sequence != 2 || page.Phases[1].ExecutionID != execB || page.Phases[1].Sequence != 1 {
		t.Fatalf("order=%+v", page.Phases)
	}
	if page.NextAfter != 2 || page.Phases[0].DurationMS == nil || *page.Phases[0].DurationMS != duration || page.Phases[0].CreatedAt.IsZero() {
		t.Fatalf("page=%+v", page)
	}
	filtered, err := reader.ListRunPhases(ctx, user, session.ID, run.ID, 1)
	if err != nil || len(filtered.Phases) != 1 || filtered.Phases[0].Sequence != 2 {
		t.Fatalf("after=1 %+v %v", filtered, err)
	}
	assertPhasePayload(t, page, secret)
	if _, err = reader.ListRunPhases(ctx, "other-"+user, session.ID, run.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatched user: %v", err)
	}
	if _, err = store.GetRun(ctx, "other-"+user, session.ID, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRun mismatch: %v", err)
	}
	if _, err = reader.ListRunPhases(ctx, user, session.ID, uuid.NewString(), 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run: %v", err)
	}

	pagedRun, err := store.Enqueue(ctx, user, session.ID, secret, "page-key")
	if err != nil {
		t.Fatal(err)
	}
	execution := uuid.NewString()
	for seq := int64(1); seq <= int64(phasePageLimit)+1; seq++ {
		if err = store.AppendRunPhase(ctx, RunPhase{RunID: pagedRun.ID, ExecutionID: execution, Sequence: seq, SpanID: seq, Phase: "tool", Name: "calculator", Event: "started"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := reader.ListRunPhases(ctx, user, session.ID, pagedRun.ID, 0)
	if err != nil || len(first.Phases) != phasePageLimit || first.NextAfter != int64(phasePageLimit) || first.Phases[0].Sequence != 1 || first.Phases[len(first.Phases)-1].Sequence != int64(phasePageLimit) {
		t.Fatalf("first page len=%d next=%d err=%v", len(first.Phases), first.NextAfter, err)
	}
	second, err := reader.ListRunPhases(ctx, user, session.ID, pagedRun.ID, first.NextAfter)
	if err != nil || len(second.Phases) != 1 || second.Phases[0].Sequence != int64(phasePageLimit)+1 {
		t.Fatalf("second page=%+v err=%v", second.Phases, err)
	}
}

func assertPhasePayload(t *testing.T, page RunPhasePage, secret string) {
	t.Helper()
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("phase payload leaked %q: %s", secret, raw)
	}
	var body struct {
		Phases []map[string]any `json:"phases"`
	}
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"run_id": true, "execution_id": true, "sequence": true, "span_id": true, "parent_span_id": true,
		"phase": true, "name": true, "event": true, "status": true, "duration_ms": true, "error": true, "created_at": true,
	}
	for _, phase := range body.Phases {
		for key := range phase {
			if !allowed[key] {
				t.Fatalf("unexpected phase field %q in %s", key, raw)
			}
		}
	}
}
