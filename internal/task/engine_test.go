package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/internal/testutil"
	"easygo-agent/internal/toolregistry"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

var crash = errors.New("injected process crash")

type crashStore struct {
	Store
	match func(Task) bool
	after bool
	fired bool
}

func (s *crashStore) Save(ctx context.Context, t Task, token string) error {
	if !s.fired && s.match(t) {
		s.fired = true
		if s.after {
			if err := s.Store.Save(ctx, t, token); err != nil {
				return err
			}
		}
		return crash
	}
	return s.Store.Save(ctx, t, token)
}
func callMessage(id, name, args string) *schema.AgenticMessage {
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{CallID: id, Name: name, Arguments: args})}}
}
func fixture(t *testing.T, s Store, retry toolregistry.Retry) (*Engine, Task, *atomic.Int32) {
	t.Helper()
	ctx := context.Background()
	count := &atomic.Int32{}
	r := toolregistry.New()
	tool, err := utils.InferTool("business", "test capability", func(ctx context.Context, in struct{}) (map[string]any, error) {
		count.Add(1)
		if retry == toolregistry.Idempotent && toolregistry.IdempotencyKey(ctx) == "" {
			return nil, errors.New("missing idempotency key")
		}
		return map[string]any{"ok": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Register(ctx, tool, "business", retry, false); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Roles = map[string]Role{"executor": {Instruction: "execute", Tools: []string{"business"}}}
	engine := &Engine{Store: s, Registry: r, Config: cfg, SkillVersion: "skills-v1", Model: &testutil.Model{GenerateFunc: func(ctx context.Context, in []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		for _, m := range in {
			for _, b := range m.ContentBlocks {
				if b.FunctionToolResult != nil && b.FunctionToolResult.Name == "business" {
					if b.FunctionToolResult.CallID != "c1" {
						t.Error("lost call ID")
					}
					return callMessage("done", "finish_task", `{"summary":"verified","evidence":["business returned ok"]}`), nil
				}
			}
		}
		return callMessage("c1", "business", "{}"), nil
	}}}
	created, err := s.Create(ctx, Owner{User: "u", Session: "s", Run: "r"}, Brief{Role: "executor", Goal: "work", Acceptance: []string{"verified"}, Key: "one"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != created.ID {
		t.Fatal("wrong claim")
	}
	return engine, claimed, count
}
func TestRecoveryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		retry toolregistry.Retry
		kind  string
		after bool
		want  Status
		calls int32
	}{
		{"after-model", toolregistry.Unsafe, "model_output", true, Completed, 1},
		{"before-write-result", toolregistry.Unsafe, "tool_result", false, Blocked, 1},
		{"after-write-result", toolregistry.Unsafe, "tool_result", true, Completed, 1},
		{"readonly-retry", toolregistry.ReadOnly, "tool_result", false, Completed, 2},
		{"idempotent-retry", toolregistry.Idempotent, "tool_result", false, Completed, 2},
		{"registered-write-not-invoked", toolregistry.Unsafe, "tool_started", true, Blocked, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := NewMemory()
			fault := &crashStore{Store: base, after: tc.after, match: func(task Task) bool { return len(task.Events) > 0 && task.Events[len(task.Events)-1].Kind == tc.kind }}
			e, claimed, count := fixture(t, fault, tc.retry)
			if err := e.Execute(ctx, claimed); !errors.Is(err, crash) {
				t.Fatalf("want crash, got %v", err)
			}
			if err := base.Release(ctx, claimed.ID, claimed.Token); err != nil {
				t.Fatal(err)
			}
			recovered, err := base.Claim(ctx, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			e.Store = base
			if err = e.Execute(ctx, recovered); err != nil {
				t.Fatal(err)
			}
			got, err := base.Get(ctx, claimed.Owner, claimed.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want || count.Load() != tc.calls {
				t.Fatalf("status=%s calls=%d want %s/%d", got.Status, count.Load(), tc.want, tc.calls)
			}
			if got.Status == Completed && len(got.Calls) != 2 {
				t.Fatalf("call audit lost: %+v", got.Calls)
			}
		})
	}
}
func TestSameBatchReusesSavedResultAndRetriesUnfinishedRead(t *testing.T) {
	ctx := context.Background()
	base := NewMemory()
	var writes, reads atomic.Int32
	r := toolregistry.New()
	write, err := utils.InferTool("write_a", "idempotent write", func(ctx context.Context, in struct{}) (string, error) {
		if toolregistry.IdempotencyKey(ctx) == "" {
			return "", errors.New("missing idempotency key")
		}
		return fmt.Sprintf("persisted-%d", writes.Add(1)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := utils.InferTool("read_b", "read only", func(ctx context.Context, in struct{}) (string, error) {
		return fmt.Sprintf("observed-%d", reads.Add(1)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Register(ctx, write, "business", toolregistry.Idempotent, false); err != nil {
		t.Fatal(err)
	}
	if err = r.Register(ctx, read, "business", toolregistry.ReadOnly, false); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Roles = map[string]Role{"executor": {Instruction: "execute", Tools: []string{"write_a", "read_b"}}}
	fault := &crashStore{Store: base, match: func(task Task) bool {
		if len(task.Events) == 0 {
			return false
		}
		last := task.Events[len(task.Events)-1]
		return last.Kind == "tool_result" && strings.HasPrefix(last.Detail, "call-b ")
	}}
	engine := &Engine{Store: fault, Registry: r, Config: cfg, SkillVersion: "skills-v1", Model: &testutil.Model{GenerateFunc: func(ctx context.Context, in []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		seen := map[string]int{}
		for _, m := range in {
			if m == nil {
				continue
			}
			for _, b := range m.ContentBlocks {
				if b != nil && b.FunctionToolResult != nil {
					seen[b.FunctionToolResult.CallID]++
				}
			}
		}
		if seen["call-a"] > 1 || seen["call-b"] > 1 {
			t.Errorf("tool result paired more than once: %+v", seen)
		}
		if seen["call-a"] == 1 && seen["call-b"] == 1 {
			return callMessage("done", "finish_task", `{"summary":"verified","evidence":["write_a reused","read_b completed"]}`), nil
		}
		if seen["call-a"] == 1 || seen["call-b"] == 1 {
			t.Errorf("model continued before both call IDs were paired: %+v", seen)
		}
		return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-a", Name: "write_a", Arguments: "{}"}),
			schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-b", Name: "read_b", Arguments: "{}"}),
		}}, nil
	}}}
	if _, err = base.Create(ctx, Owner{User: "u", Session: "s", Run: "r"}, Brief{Role: "executor", Goal: "work", Acceptance: []string{"verified"}, Key: "batch"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := base.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Execute(ctx, claimed); !errors.Is(err, crash) {
		t.Fatalf("want crash, got %v", err)
	}
	mid, err := base.Get(ctx, claimed.Owner, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	savedA, savedB := callByID(mid.Checkpoint.Calls, "call-a"), callByID(mid.Checkpoint.Calls, "call-b")
	if savedA == nil || savedB == nil || !savedA.Done || savedA.Result == "" || savedB.Done || !savedB.Started {
		t.Fatalf("pre-crash batch A=%+v B=%+v", savedA, savedB)
	}
	savedResult := savedA.Result
	if err = base.Release(ctx, claimed.ID, claimed.Token); err != nil {
		t.Fatal(err)
	}
	recovered, err := base.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	engine.Store = base
	if err = engine.Execute(ctx, recovered); err != nil {
		t.Fatal(err)
	}
	got, err := base.Get(ctx, claimed.Owner, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Completed {
		t.Fatalf("status=%s reason=%s", got.Status, got.Reason)
	}
	if writes.Load() != 1 || reads.Load() != 2 {
		t.Fatalf("write_a calls=%d read_b calls=%d", writes.Load(), reads.Load())
	}
	finalA, finalB := callByID(got.Calls, "call-a"), callByID(got.Calls, "call-b")
	if finalA == nil || finalB == nil {
		t.Fatalf("call audit lost: %+v", got.Calls)
	}
	if finalA.Result != savedResult {
		t.Fatalf("saved result bytes changed: %q -> %q", savedResult, finalA.Result)
	}
	if !finalB.Done || finalB.Result == "" {
		t.Fatalf("read result was not saved: %+v", finalB)
	}
}
func callByID(calls []Call, id string) *Call {
	for i := range calls {
		if calls[i].ID == id {
			return &calls[i]
		}
	}
	return nil
}
func TestResumeUncertainWriteWithVerifiedResult(t *testing.T) {
	ctx := context.Background()
	base := NewMemory()
	fault := &crashStore{Store: base, match: func(t Task) bool { return len(t.Events) > 0 && t.Events[len(t.Events)-1].Kind == "tool_result" }}
	e, claimed, count := fixture(t, fault, toolregistry.Unsafe)
	_ = e.Execute(ctx, claimed)
	_ = base.Release(ctx, claimed.ID, claimed.Token)
	recovered, _ := base.Claim(ctx, time.Minute)
	e.Store = base
	if err := e.Execute(ctx, recovered); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Resume(ctx, claimed.Owner, claimed.ID, Resume{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unguarded resume: %v", err)
	}
	raw := `{"ok":true}`
	resumed, err := base.Resume(ctx, claimed.Owner, claimed.ID, Resume{Instructions: "also preserve evidence", Decisions: map[string]Decision{"c1": {Action: "result", Result: &raw}}})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Version != 2 || resumed.History[0].Checkpoint.Calls[0].Done {
		t.Fatal("resume rewrote prior execution")
	}
	again, _ := base.Claim(ctx, time.Minute)
	if err = e.Execute(ctx, again); err != nil {
		t.Fatal(err)
	}
	got, _ := base.Get(ctx, claimed.Owner, claimed.ID)
	if got.Status != Completed || count.Load() != 1 {
		t.Fatalf("%s calls=%d", got.Status, count.Load())
	}
	pending, _ := base.Pending(ctx)
	if len(pending) != 2 {
		t.Fatalf("outbox: %+v", pending)
	}
}
func TestRecoveryBlocksConfigurationChange(t *testing.T) {
	ctx := context.Background()
	base := NewMemory()
	fault := &crashStore{Store: base, after: true, match: func(t Task) bool { return len(t.Checkpoint.Calls) > 0 }}
	e, claimed, count := fixture(t, fault, toolregistry.ReadOnly)
	_ = e.Execute(ctx, claimed)
	_ = base.Release(ctx, claimed.ID, claimed.Token)
	again, _ := base.Claim(ctx, time.Minute)
	e.Store = base
	e.SkillVersion = "changed"
	if err := e.Execute(ctx, again); err != nil {
		t.Fatal(err)
	}
	got, _ := base.Get(ctx, claimed.Owner, claimed.ID)
	if got.Status != Blocked || count.Load() != 0 {
		t.Fatalf("incompatible checkpoint ran: %+v", got)
	}
}
func TestMemoryOwnershipDedupLeaseAndResume(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	o := Owner{"u", "s", "r"}
	b := Brief{Role: "executor", Goal: "x", Acceptance: []string{"x"}, Key: "key"}
	one, err := s.Create(ctx, o, b)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := s.Create(ctx, o, b)
	if one.ID != two.ID {
		t.Fatal("duplicate task")
	}
	b.Goal = "different"
	if _, err = s.Create(ctx, o, b); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, Owner{"other", "s", "r"}, one.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("owner leak")
	}
	var wins atomic.Int32
	var winner Task
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, e := s.Claim(ctx, 20*time.Millisecond); e == nil {
				wins.Add(1)
				mu.Lock()
				winner = c
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claims=%d", wins.Load())
	}
	if _, err = s.Resume(ctx, o, one.ID, Resume{}); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent resume accepted")
	}
	time.Sleep(25 * time.Millisecond)
	next, err := s.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(ctx, winner, winner.Token); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("stale write accepted")
	}
	if next.Token == winner.Token {
		t.Fatal("token reused")
	}
	if _, err = s.Cancel(ctx, o, one.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Heartbeat(ctx, next.ID, next.Token, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("canceled worker renewed")
	}
}
func TestBudgetsSurviveRecovery(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			ctx := context.Background()
			s := NewMemory()
			e, c, _ := fixture(t, s, toolregistry.ReadOnly)
			_, version, _ := e.role(ctx, "executor")
			c.Checkpoint = Checkpoint{Format: FormatVersion, Version: version, Steps: e.Config.MaxSteps, Deadline: time.Now().Add(time.Minute)}
			if timeout {
				c.Checkpoint.Steps = 0
				c.Checkpoint.Deadline = time.Now().Add(-time.Second)
			}
			if err := s.Save(ctx, c, c.Token); err != nil {
				t.Fatal(err)
			}
			if err := e.Execute(ctx, c); err != nil {
				t.Fatal(err)
			}
			got, _ := s.Get(ctx, c.Owner, c.ID)
			if got.Status != Failed {
				t.Fatal(got.Status)
			}
		})
	}
}
func TestRoleDenyRules(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	e, _, _ := fixture(t, s, toolregistry.Unsafe)
	e.Config.Roles["analyst"] = Role{Tools: []string{"business"}}
	if err := e.Validate(ctx); err == nil {
		t.Fatal("analyst can write")
	}
	delete(e.Config.Roles, "analyst")
	entry, _ := e.Registry.Lookup("business")
	entry.Info.Name = "sandbox_write"
	_ = e.Registry.Register(ctx, entry.Tool, "sandbox", toolregistry.ReadOnly, false)
	e.Config.Roles["executor"] = Role{Tools: []string{"sandbox_write"}}
	if err := e.Validate(ctx); err == nil {
		t.Fatal("sandbox exposed")
	}
}

func TestMemoryOutboxRemainsImmutableWithinVersion(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	e, c, _ := fixture(t, s, toolregistry.ReadOnly)
	if err := e.Execute(ctx, c); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.Pending(ctx)
	if len(pending) != 1 {
		t.Fatal("missing notification")
	}
	id := pending[0].ID
	if err := s.MarkEnqueued(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdatePlan(ctx, c.Owner, c.ID, []PlanStep{{Text: "reviewed", Status: "completed"}}); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.Pending(ctx)
	if len(pending) != 0 {
		t.Fatal("terminal mutation reenqueued notification")
	}
}

func TestTimeoutDuringWriteRequiresExplicitRecovery(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	e, c, _ := fixture(t, s, toolregistry.Unsafe)
	r := toolregistry.New()
	write, _ := utils.InferTool("business", "uncertain write", func(ctx context.Context, in struct{}) (string, error) { <-ctx.Done(); return "", ctx.Err() })
	_ = r.Register(ctx, write, "business", toolregistry.Unsafe, false)
	e.Registry = r
	e.Config.Timeout = 15 * time.Millisecond
	if err := e.Execute(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, c.Owner, c.ID)
	if got.Status != Blocked || len(got.Checkpoint.Calls) != 1 || got.Checkpoint.Calls[0].Done {
		t.Fatalf("lost uncertainty: %+v", got)
	}
}
func TestResumeKeepsInitialRequestIdempotent(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	e, c, _ := fixture(t, s, toolregistry.ReadOnly)
	if err := e.Execute(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resume(ctx, c.Owner, c.ID, Resume{Instructions: "new constraints"}); err != nil {
		t.Fatal(err)
	}
	again, err := s.Create(ctx, c.Owner, c.Brief)
	if err != nil || again.ID != c.ID {
		t.Fatalf("original retry changed identity: %+v %v", again, err)
	}
}
