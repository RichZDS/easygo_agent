package deepagent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/internal/agent/chatmodel"
	"easygo-agent/internal/agent/deepagent"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/tools"
	"github.com/cloudwego/eino/components/tool"
)

// A real local SSE wire proves production runtime -> native loop -> model
// adapter -> gateway -> tool result -> next model -> final persistence.
func TestNativeRuntimeThroughGatewayHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"calc-1\",\"type\":\"function\",\"function\":{\"name\":\"calculator\",\"arguments\":\"{\\\"operation\\\":\\\"add\\\",\\\"a\\\":2,\\\"b\\\":3}\"}}]}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
		} else {
			messages := req["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "calc-1" || !strings.Contains(fmt.Sprint(last["content"]), "5") {
				t.Errorf("missing tool result: %+v", last)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"The answer is 5.\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":8}}\n\ndata: [DONE]\n\n")
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := chatmodel.New(ctx, config.ModelConfig{Name: "fixture", BaseURL: server.URL + "/v1", APIKey: "fixture", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: m, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 3, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	store := conversation.NewMemory()
	session, _ := store.Create(ctx, "alice")
	run := startRun(t, ctx, store, agent, "alice", session.ID, "2+3?")
	events := collect(t, run)
	defer run.Close()
	if last := events[len(events)-1]; last.Kind != agentruntime.EventCompleted {
		t.Fatalf("terminal=%+v", last)
	}
	turns, err := store.History(ctx, "alice", session.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || len(turns) != 1 || len(turns[0].Messages) != 4 || turns[0].Messages[3].ResponseMeta.TokenUsage.TotalTokens != 58 {
		t.Fatalf("lost transcript: requests=%d turns=%+v", requests.Load(), turns)
	}
}

func TestNativeRuntimeCancelClosesGatewayStream(t *testing.T) {
	upstreamDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model, err := chatmodel.New(ctx, config.ModelConfig{Name: "fixture", BaseURL: server.URL + "/v1", APIKey: "fixture", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Agent: config.AgentConfig{MaxSteps: 2, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	store := conversation.NewMemory()
	session, _ := store.Create(ctx, "alice")
	run := startRun(t, ctx, store, agent, "alice", session.ID, "go")
	for {
		event := run.Next()
		if event.Kind == agentruntime.EventTextDelta {
			break
		}
		if event.IsTerminal() {
			t.Fatalf("terminated before partial output: %+v", event)
		}
	}
	run.Cancel()
	terminal := run.Close()
	if terminal.Kind != agentruntime.EventCanceled {
		t.Fatalf("cancellation became success: %+v", terminal)
	}
	select {
	case <-upstreamDone:
	case <-ctx.Done():
		t.Fatal("gateway request remained alive after cancel")
	}
}
