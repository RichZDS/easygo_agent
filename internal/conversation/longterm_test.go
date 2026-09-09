package conversation

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestRankMemoriesUsesEqualFreshnessAndCallWeights(t *testing.T) {
	now := time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC)
	memories := []LongTermMemory{
		{ID: "fresh", State: memoryStateActive, LastSeenAt: now, CallCount: 0},
		{ID: "used", State: memoryStateActive, LastSeenAt: now.AddDate(0, 0, -90), CallCount: 9},
	}
	ranked := RankMemories(memories, now)
	byID := map[string]LongTermMemory{}
	for _, memory := range ranked {
		byID[memory.ID] = memory
	}
	// fresh: 0.5*1 + 0.5*0; used: 0.5*0.5 + 0.5*1.
	if math.Abs(byID["fresh"].Score-.5) > .000001 || math.Abs(byID["used"].Score-.75) > .000001 || ranked[0].ID != "used" {
		t.Fatalf("equal-weight ranking=%+v", ranked)
	}
}

func TestMemoryProfileIsCappedAtFiveAndRecallCountsOnlySelected(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	drafts := make([]MemoryDraft, 0, MaxProfileMemories)
	for i := 0; i < MaxProfileMemories; i++ {
		drafts = append(drafts, MemoryDraft{Kind: MemoryKindPreference, Content: string(rune('a' + i)), Importance: .8, Confidence: .9, SourceSessions: []string{"s"}, SourceTurnIDs: []int64{1}})
	}
	profile, err := store.ReplaceProfile(ctx, "alice", drafts)
	if err != nil || len(profile) != MaxProfileMemories {
		t.Fatalf("replace profile=%+v err=%v", profile, err)
	}
	if _, err = store.ReplaceProfile(ctx, "alice", append(drafts, drafts[0])); err != ErrTooManyProfileMemories {
		t.Fatalf("six profile rows err=%v", err)
	}
	recalled, err := store.Recall(ctx, "alice", 2)
	if err != nil || len(recalled) != 2 {
		t.Fatalf("recall=%+v err=%v", recalled, err)
	}
	active, _ := store.ActiveProfile(ctx, "alice")
	called := 0
	for _, memory := range active {
		if memory.CallCount == 1 {
			called++
		} else if memory.CallCount != 0 {
			t.Fatalf("unexpected call count: %+v", memory)
		}
	}
	if called != 2 {
		t.Fatalf("selected recall was not counted exactly twice: %+v", active)
	}
}

func TestMemoryJobLeasePreventsDuplicateConsolidation(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	first, acquired, err := store.TryAcquireMemoryJob(ctx, "alice")
	if err != nil || !acquired {
		t.Fatalf("first lease acquired=%v err=%v", acquired, err)
	}
	if second, acquired, err := store.TryAcquireMemoryJob(ctx, "alice"); err != nil || acquired || second != nil {
		t.Fatalf("duplicate lease=%v acquired=%v err=%v", second, acquired, err)
	}
	first.Close()
	last, acquired, err := store.TryAcquireMemoryJob(ctx, "alice")
	if err != nil || !acquired {
		t.Fatalf("lease was not released: acquired=%v err=%v", acquired, err)
	}
	last.Close()
}
