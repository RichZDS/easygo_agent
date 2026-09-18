package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/internal/agent/deepagent"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/prompt"
	"easygo-agent/internal/task"
	"easygo-agent/internal/testutil"
	"easygo-agent/internal/toolregistry"
	"easygo-agent/internal/tools"
	"github.com/cloudwego/eino/schema"
)

func TestDelegationCompletesAndSummarizesWithLatestConstraints(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, _ := store.Create(ctx, "alice")
	tasks := task.NewMemory()
	cfg := task.DefaultConfig()
	cfg.PollInterval = time.Millisecond
	cfg.Roles = map[string]task.Role{"analyst": {}}
	gate := make(chan struct{})
	childModel := &testutil.Model{GenerateFunc: func(ctx context.Context, in []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-gate:
		}
		return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{CallID: "finish", Name: "finish_task", Arguments: `{"summary":"verified background result","evidence":["checked acceptance"]}`})}}, nil
	}}
	engine := &task.Engine{Store: tasks, Registry: toolregistry.New(), Config: cfg, Model: childModel}
	service := &task.Service{Store: tasks, Engine: engine, Conversations: store}
	mainTools, err := tools.NewTaskTools(service)
	if err != nil {
		t.Fatal(err)
	}
	var summaries atomic.Int32
	var latest atomic.Bool
	var sawSystem atomic.Bool
	mainModel := &testutil.Model{GenerateFunc: func(ctx context.Context, in []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		identity, _ := agentruntime.InvocationIdentityFromContext(ctx)
		if identity.Internal {
			summaries.Add(1)
			for _, m := range in {
				raw, _ := json.Marshal(m)
				if strings.Contains(string(raw), "new constraint: concise") {
					latest.Store(true)
				}
				if m.Role == schema.AgenticRoleTypeSystem && strings.Contains(string(raw), "Internal background task notification") {
					sawSystem.Store(true)
				}
			}
			return testutil.Text("concise verified summary"), nil
		}
		for _, m := range in {
			for _, b := range m.ContentBlocks {
				if b.FunctionToolResult != nil && b.FunctionToolResult.Name == "spawn_subagent" {
					return testutil.Text("accepted background task"), nil
				}
				if b.UserInputText != nil && strings.Contains(b.UserInputText.Text, "new constraint") {
					return testutil.Text("constraint retained"), nil
				}
			}
		}
		return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{CallID: "spawn", Name: "spawn_subagent", Arguments: `{"role":"analyst","goal":"bounded work","acceptance":["checked"],"idempotency_key":"one"}`})}}, nil
	}}
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: mainModel, Tools: mainTools, Agent: config.AgentConfig{MaxSteps: 5, ContextTokens: 24000}, Instruction: prompt.SystemPrompt})
	if err != nil {
		t.Fatal(err)
	}
	queue := agentruntime.NewQueueManager(ctx, store, agent, agentruntime.QueueConfig{MaxWorkers: 1, PollInterval: time.Millisecond, LeaseTTL: time.Second, Tasks: service})
	defer queue.Close()
	if err = service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	first, _, err := queue.Submit(ctx, "alice", session.ID, "delegate", "")
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, queue, "alice", session.ID, first.ID)
	second, _, err := queue.Submit(ctx, "alice", session.ID, "new constraint: concise", "")
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, queue, "alice", session.ID, second.ID)
	close(gate)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := store.NotificationRuns(ctx, "alice", session.ID, 0)
		if len(runs) == 1 && runs[0].Status == conversation.RunCompleted {
			if summaries.Load() != 1 || !latest.Load() || !sawSystem.Load() {
				t.Fatalf("summaries=%d latest=%v internal=%v", summaries.Load(), latest.Load(), sawSystem.Load())
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("automatic summary was not committed")
}
func waitRun(t *testing.T, q agentruntime.QueueManager, user, session, id string) {
	t.Helper()
	sub, err := q.Subscribe(context.Background(), user, session, id)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for {
		select {
		case event := <-sub.Events():
			if event.IsTerminal() {
				if event.Kind != agentruntime.EventCompleted {
					t.Fatalf("run failed: %+v", event)
				}
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatal("run timeout")
		}
	}
}
