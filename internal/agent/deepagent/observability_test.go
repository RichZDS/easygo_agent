package deepagent_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"easygo-agent/internal/agent/deepagent"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"
	"easygo-agent/internal/tools"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// Real Eino middleware and runtime, with an in-process provider adapter.
func TestLoopTraceCorrelatesModelToolRecoveryAndCommit(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx := telemetry.WithLogger(context.Background(), zap.New(core))
	store := conversation.NewMemory()
	session, _ := store.Create(ctx, "alice")
	var calls atomic.Int32
	m := &testutil.Model{GenerateFunc: func(_ context.Context, messages []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		if calls.Add(1) == 1 {
			return testutil.ToolCall("divide-call", `{"operation":"divide","a":1,"b":0}`), nil
		}
		result := messages[len(messages)-1].ContentBlocks[0].FunctionToolResult
		if result == nil || !strings.Contains(result.Content[0].Text.Text, "[tool error]") {
			return nil, errors.New("lost recoverable tool error")
		}
		return testutil.Text("Please use a nonzero divisor"), nil
	}}
	calculator, err := tools.NewCalculator()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: m, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 4, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	run := startRun(t, ctx, store, agent, "alice", session.ID, "private request")
	events := collect(t, run)
	if events[len(events)-1].Kind != agentruntime.EventCompleted {
		t.Fatalf("terminal=%+v", events[len(events)-1])
	}
	run.Close()
	runID := events[len(events)-1].RunID
	counts := map[string]int{}
	var execution string
	var rootID uint64
	for _, entry := range logs.FilterMessage("agent.phase").All() {
		f := entry.ContextMap()
		if f["session_id"] != session.ID || f["run_id"] != runID {
			t.Fatalf("uncorrelated phase: %v", f)
		}
		if execution == "" {
			execution = f["execution_id"].(string)
			rootID = f["span_id"].(uint64)
		}
		if f["execution_id"] != execution {
			t.Fatalf("execution drift: %v", f)
		}
		if f["event"] != "finished" {
			continue
		}
		phase := f["phase"].(string)
		counts[phase]++
		if phase != "run" && f["parent_span_id"] != rootID {
			t.Fatalf("unexpected parent: %v", f)
		}
		if phase == "tool" && (f["status"] != "recovered" || f["call_id"] != "divide-call") {
			t.Fatalf("tool recovery misreported: %v", f)
		}
		if phase == "run" && (f["model_calls"] != int64(2) || f["tool_calls"] != int64(1) || f["status"] != "completed") {
			t.Fatalf("bad summary: %v", f)
		}
	}
	if counts["model"] != 2 || counts["tool"] != 1 || counts["memory"] != 1 || counts["persist"] != 1 || counts["run"] != 1 || counts["budget"] != 2 {
		t.Fatalf("missing phases: %v", counts)
	}
}

func TestFailedCompressionTracesSummaryModelWithoutCallingMain(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx := telemetry.WithLogger(context.Background(), zap.New(core))
	summary := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		return nil, errors.New("summary provider failed")
	}}
	main := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		t.Error("main called after failed compression")
		return testutil.Text("wrong"), nil
	}}
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: main, SummaryModel: summary, Agent: config.AgentConfig{MaxSteps: 2, ContextTokens: 2000}})
	if err != nil {
		t.Fatal(err)
	}
	store := conversation.NewMemory()
	session, _ := store.Create(ctx, "alice")
	run := startRun(t, ctx, store, agent, "alice", session.ID, strings.Repeat("context ", 2000))
	events := collect(t, run)
	run.Close()
	if events[len(events)-1].Kind != agentruntime.EventFailed {
		t.Fatal(events[len(events)-1])
	}
	finished := logs.FilterMessage("agent.phase").FilterField(zap.String("event", "finished")).All()
	phases := map[string]map[string]any{}
	for _, e := range finished {
		f := e.ContextMap()
		phases[f["phase"].(string)] = f
	}
	for _, phase := range []string{"compression", "budget", "model", "run"} {
		if phases[phase]["status"] != "failed" {
			t.Fatalf("missing failed %s: %v", phase, phases)
		}
	}
	if phases["model"]["parent_span_id"] != phases["compression"]["span_id"] {
		t.Fatal("summary model lost parent compression")
	}
}
