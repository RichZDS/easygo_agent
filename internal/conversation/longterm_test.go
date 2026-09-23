package conversation

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestMemoryDoesNotImplementMemoryStore(t *testing.T) {
	var store any = NewMemory()
	if _, ok := store.(MemoryStore); ok {
		t.Fatal("*Memory still implements MemoryStore")
	}
}

func testMemoryStore(t *testing.T, store MemoryStore, user string) {
	t.Helper()
	ctx := context.Background()
	drafts := make([]MemoryDraft, 0, MaxProfileMemories)
	for i := 0; i < MaxProfileMemories; i++ {
		drafts = append(drafts, MemoryDraft{Kind: MemoryKindPreference, Content: string(rune('a' + i)), Importance: .8, Confidence: .9, SourceSessions: []string{"s"}, SourceTurnIDs: []int64{1}})
	}
	profile, err := store.ReplaceProfile(ctx, user, drafts)
	if err != nil || len(profile) != MaxProfileMemories {
		t.Fatalf("replace profile=%+v err=%v", profile, err)
	}
	if _, err = store.ReplaceProfile(ctx, user, append(drafts, drafts[0])); err != ErrTooManyProfileMemories {
		t.Fatalf("six profile rows err=%v", err)
	}
	recalled, err := store.Recall(ctx, user, 2)
	if err != nil || len(recalled) != 2 {
		t.Fatalf("recall=%+v err=%v", recalled, err)
	}
	active, _ := store.ActiveProfile(ctx, user)
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
	first, acquired, err := store.TryAcquireMemoryJob(ctx, user)
	if err != nil || !acquired {
		t.Fatalf("first lease acquired=%v err=%v", acquired, err)
	}
	if second, acquired, err := store.TryAcquireMemoryJob(ctx, user); err != nil || acquired || second != nil {
		t.Fatalf("duplicate lease=%v acquired=%v err=%v", second, acquired, err)
	}
	first.Close()
	last, acquired, err := store.TryAcquireMemoryJob(ctx, user)
	if err != nil || !acquired {
		t.Fatalf("lease was not released: acquired=%v err=%v", acquired, err)
	}
	last.Close()
}

func TestMemoryLongTermContract(t *testing.T) {
	store := NewMemoryLongTerm()
	t.Cleanup(store.Close)
	testMemoryStore(t, store, "alice")
}

func TestMemoryPostgresContract(t *testing.T) {
	dsn := os.Getenv("EASYGO_TEST_MEMORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("set EASYGO_TEST_MEMORY_DATABASE_URL for memory PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := NewMemoryPostgres(ctx, dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	testMemoryStore(t, store, "mem-"+uuid.NewString())
}

func TestMemoryLongTermSupersession(t *testing.T) {
	store := NewMemoryLongTerm()
	t.Cleanup(store.Close)
	testSupersession(t, store, "alice", "bob")
}

func TestMemoryPostgresSupersession(t *testing.T) {
	dsn := os.Getenv("EASYGO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set EASYGO_TEST_DATABASE_URL for PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := NewMemoryPostgres(ctx, dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	user := "mem-" + uuid.NewString()
	other := "mem-" + uuid.NewString()
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanCancel()
		_, _ = store.pool.Exec(cleanCtx, `DELETE FROM user_long_term_memories WHERE username=$1 OR username=$2`, user, other)
	})
	testSupersession(t, store, user, other)
}

func profileDraft(id, content string, kind MemoryKind, tags []string, importance float64) MemoryDraft {
	return MemoryDraft{
		ID: id, Kind: kind, Content: content, Tags: tags,
		Importance: importance, Confidence: .9,
		SourceSessions: []string{"session"}, SourceTurnIDs: []int64{1},
	}
}

func testSupersession(t *testing.T, store MemoryStore, user, other string) {
	t.Helper()
	ctx := context.Background()
	created, err := store.ReplaceProfile(ctx, user, []MemoryDraft{
		profileDraft("", "old-a", MemoryKindPreference, []string{"t1"}, .5),
		profileDraft("", "keep-b", MemoryKindPreference, []string{"t2"}, .5),
	})
	if err != nil || len(created) != 2 || created[0].Content != "old-a" || created[0].Version != 1 {
		t.Fatalf("create profile=%+v err=%v", created, err)
	}
	survivingID := created[0].ID
	retiredID := created[1].ID

	updated, err := store.ReplaceProfile(ctx, user, []MemoryDraft{
		profileDraft(survivingID, "new-a", MemoryKindPreference, []string{"t1"}, .5),
	})
	if err != nil || len(updated) != 1 || updated[0].ID != survivingID || updated[0].Version != 2 || updated[0].SupersededBy != nil {
		t.Fatalf("content replace=%+v err=%v", updated, err)
	}
	history, err := store.History(ctx, user, survivingID)
	if err != nil || len(history) != 1 || history[0].Content != "old-a" || history[0].Version != 1 || history[0].ID == survivingID {
		t.Fatalf("content history=%+v err=%v", history, err)
	}
	assertSnapshot(t, history[0], user, survivingID)

	time.Sleep(5 * time.Millisecond)
	updated, err = store.ReplaceProfile(ctx, user, []MemoryDraft{
		profileDraft(survivingID, "new-a", MemoryKindPreference, []string{"t1"}, .8),
	})
	if err != nil || updated[0].Version != 3 {
		t.Fatalf("importance replace=%+v err=%v", updated, err)
	}
	history, err = store.History(ctx, user, survivingID)
	if err != nil || len(history) != 1 {
		t.Fatalf("importance change created a snapshot: %+v err=%v", history, err)
	}

	time.Sleep(5 * time.Millisecond)
	updated, err = store.ReplaceProfile(ctx, user, []MemoryDraft{
		profileDraft(survivingID, "new-a", MemoryKindStyle, []string{"t1"}, .8),
	})
	if err != nil || updated[0].Version != 4 || updated[0].Kind != MemoryKindStyle {
		t.Fatalf("kind replace=%+v err=%v", updated, err)
	}

	time.Sleep(5 * time.Millisecond)
	updated, err = store.ReplaceProfile(ctx, user, []MemoryDraft{
		profileDraft(survivingID, "new-a", MemoryKindStyle, []string{"t1", "t3"}, .8),
	})
	if err != nil || len(updated) != 1 || updated[0].Version != 5 || updated[0].SupersededBy != nil {
		t.Fatalf("tag replace=%+v err=%v", updated, err)
	}
	history, err = store.History(ctx, user, survivingID)
	if err != nil || len(history) != 3 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	if history[0].Content != "old-a" || history[0].Kind != MemoryKindPreference || history[0].Version != 1 || !sameStrings(history[0].Tags, []string{"t1"}) {
		t.Fatalf("oldest snapshot=%+v", history[0])
	}
	if history[1].Content != "new-a" || history[1].Kind != MemoryKindPreference || history[1].Version != 3 || history[1].Importance != .8 {
		t.Fatalf("kind snapshot=%+v", history[1])
	}
	if history[2].Content != "new-a" || history[2].Kind != MemoryKindStyle || history[2].Version != 4 || !sameStrings(history[2].Tags, []string{"t1"}) {
		t.Fatalf("tag snapshot=%+v", history[2])
	}
	for i, snapshot := range history {
		assertSnapshot(t, snapshot, user, survivingID)
		if i > 0 && snapshot.ArchivedAt != nil && history[i-1].ArchivedAt != nil && !snapshot.ArchivedAt.After(*history[i-1].ArchivedAt) {
			t.Fatalf("history not oldest-first: %+v then %+v", history[i-1], snapshot)
		}
	}
	if retiredHistory, err := store.History(ctx, user, retiredID); err != nil || len(retiredHistory) != 0 {
		t.Fatalf("retired history=%+v err=%v", retiredHistory, err)
	}
	retired := findMemory(t, archivedMemories(t, store, user), retiredID)
	if retired.SupersededBy != nil || retired.State != memoryStateArchive || retired.ProfileSlot != 0 || retired.Content != "keep-b" {
		t.Fatalf("retired row=%+v", retired)
	}
	active, err := store.ActiveProfile(ctx, user)
	if err != nil || len(active) != 1 || active[0].ID != survivingID || active[0].State != memoryStateActive || active[0].Content != "new-a" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	recalled, err := store.Recall(ctx, user, MaxProfileMemories)
	if err != nil || len(recalled) != 1 || recalled[0].ID != survivingID {
		t.Fatalf("recall=%+v err=%v", recalled, err)
	}

	otherCreated, err := store.ReplaceProfile(ctx, other, []MemoryDraft{
		profileDraft("", "other-old", MemoryKindPreference, []string{"o"}, .4),
	})
	if err != nil || len(otherCreated) != 1 {
		t.Fatalf("other create=%+v err=%v", otherCreated, err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err = store.ReplaceProfile(ctx, other, []MemoryDraft{
		profileDraft(otherCreated[0].ID, "other-new", MemoryKindPreference, []string{"o"}, .4),
	}); err != nil {
		t.Fatal(err)
	}
	if crossed, err := store.History(ctx, user, otherCreated[0].ID); err != nil || len(crossed) != 0 {
		t.Fatalf("user saw other history=%+v err=%v", crossed, err)
	}
	if crossed, err := store.History(ctx, other, survivingID); err != nil || len(crossed) != 0 {
		t.Fatalf("other saw user history=%+v err=%v", crossed, err)
	}
	otherHistory, err := store.History(ctx, other, otherCreated[0].ID)
	if err != nil || len(otherHistory) != 1 || otherHistory[0].Content != "other-old" || otherHistory[0].Username != other {
		t.Fatalf("other history=%+v err=%v", otherHistory, err)
	}
	if history, err = store.History(ctx, user, survivingID); err != nil || len(history) != 3 {
		t.Fatalf("user history changed after other user write: %+v err=%v", history, err)
	}
}

func assertSnapshot(t *testing.T, snapshot LongTermMemory, user, survivingID string) {
	t.Helper()
	if snapshot.Username != user || snapshot.State != memoryStateArchive || snapshot.ProfileSlot != 0 || snapshot.SupersededBy == nil || *snapshot.SupersededBy != survivingID || snapshot.ID == survivingID {
		t.Fatalf("snapshot=%+v surviving=%s", snapshot, survivingID)
	}
}

func findMemory(t *testing.T, memories []LongTermMemory, id string) LongTermMemory {
	t.Helper()
	for _, memory := range memories {
		if memory.ID == id {
			return memory
		}
	}
	t.Fatalf("memory %s not found in %+v", id, memories)
	return LongTermMemory{}
}

func archivedMemories(t *testing.T, store MemoryStore, user string) []LongTermMemory {
	t.Helper()
	switch s := store.(type) {
	case *MemoryLongTerm:
		s.mu.Lock()
		defer s.mu.Unlock()
		result := []LongTermMemory{}
		for _, memory := range s.longTerms[user] {
			if memory.State == memoryStateArchive {
				result = append(result, cloneLongTermMemory(memory))
			}
		}
		return result
	case *MemoryPostgres:
		rows, err := s.pool.Query(context.Background(), `SELECT `+longTermMemoryColumns+` FROM user_long_term_memories WHERE username=$1 AND state='archived'`, user)
		if err != nil {
			t.Fatal(err)
		}
		memories, err := scanLongTermMemories(rows)
		if err != nil {
			t.Fatal(err)
		}
		return memories
	default:
		t.Fatalf("unsupported store %T", store)
		return nil
	}
}
