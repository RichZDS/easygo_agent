package usermemory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easygo-agent/internal/conversation"

	"github.com/cloudwego/eino/schema"
)

type scriptedAgent struct {
	extract   []Candidate
	reconcile func([]conversation.LongTermMemory, []Candidate) []conversation.MemoryDraft
	batches   int
}

func (a *scriptedAgent) Extract(_ context.Context, _ string, _ []conversation.TranscriptTurn) ([]Candidate, error) {
	a.batches++
	return a.extract, nil
}
func (a *scriptedAgent) Reconcile(_ context.Context, _ string, current []conversation.LongTermMemory, candidates []Candidate) ([]conversation.MemoryDraft, error) {
	return a.reconcile(current, candidates), nil
}

func TestServiceConsolidatesBatchesIntoFiveSlotProfileAndStorage(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	memories := conversation.NewMemoryLongTerm()
	defer memories.Close()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Begin(ctx, "alice", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Commit(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("请一直用简洁的中文回答")}, conversation.Turn{Status: "completed", Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("请一直用简洁的中文回答")}}); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	writer, err := NewFileProfileWriter(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agent := &scriptedAgent{reconcile: func(_ []conversation.LongTermMemory, candidates []Candidate) []conversation.MemoryDraft {
		if len(candidates) != 1 || candidates[0].Content != "用户偏好简洁中文回答" {
			t.Fatalf("unexpected ranked candidates: %+v", candidates)
		}
		return []conversation.MemoryDraft{{Kind: conversation.MemoryKindStyle, Content: candidates[0].Content, Tags: candidates[0].Tags, Importance: candidates[0].Importance, Confidence: candidates[0].Confidence, SourceSessions: candidates[0].SourceSessions, SourceTurnIDs: candidates[0].SourceTurnIDs}}
	}}
	agent.extract = []Candidate{{Kind: conversation.MemoryKindStyle, Content: "用户偏好简洁中文回答", Tags: []string{"zh", "concise"}, Importance: .9, Confidence: .95, SourceSessions: []string{session.ID}, SourceTurnIDs: []int64{1}}}
	cfg := DefaultConfig()
	service, err := NewService(memories, store, agent, writer, cfg)
	if err != nil {
		t.Fatal(err)
	}
	through := time.Now().UTC().Add(time.Second)
	if err = service.ConsolidateUser(ctx, "alice", through); err != nil {
		t.Fatal(err)
	}
	if agent.batches != 1 {
		t.Fatalf("memory agent batches=%d", agent.batches)
	}
	profile, err := memories.ActiveProfile(ctx, "alice")
	if err != nil || len(profile) != 1 || profile[0].ProfileSlot != 1 || profile[0].Kind != conversation.MemoryKindStyle {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	if _, ok, err := memories.MemoryCheckpoint(ctx, "alice"); err != nil || !ok {
		t.Fatalf("checkpoint not committed: ok=%v err=%v", ok, err)
	}
	entries, err := os.ReadDir(writer.root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("profile directory: %v %v", entries, err)
	}
	style, err := os.ReadFile(filepath.Join(writer.root, entries[0].Name(), "Style.md"))
	if err != nil || !strings.Contains(string(style), "用户偏好简洁中文回答") {
		t.Fatalf("style materialization=%q err=%v", style, err)
	}
}

func TestServiceRejectsSensitiveOrUnsupportedEvidence(t *testing.T) {
	batch := []conversation.TranscriptTurn{{SessionID: "s1", Turn: conversation.Turn{ID: 7}}}
	candidates := validateCandidates([]Candidate{
		{Kind: conversation.MemoryKindPreference, Content: "API_KEY=secret-value", Importance: 1, Confidence: 1, SourceSessions: []string{"s1"}, SourceTurnIDs: []int64{7}},
		{Kind: conversation.MemoryKindPreference, Content: "valid", Importance: .8, Confidence: .8, SourceSessions: []string{"other"}, SourceTurnIDs: []int64{7}},
		{Kind: conversation.MemoryKindPreference, Content: "保留此偏好", Importance: .8, Confidence: .8, SourceSessions: []string{"s1"}, SourceTurnIDs: []int64{7}},
	}, batch)
	if len(candidates) != 1 || candidates[0].Content != "保留此偏好" {
		t.Fatalf("unsafe candidates were retained: %+v", candidates)
	}
}

func TestScheduleAlwaysSelectsTheNextThreeAM(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 9, 9, 3, 0, 0, 0, location)
	next := nextDailyRun(now, location, 3, 0)
	want := time.Date(2026, 9, 10, 3, 0, 0, 0, location)
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
}
