package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"easygo-agent/internal/agent/deepagent"
	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"
	"easygo-agent/internal/tools"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestEventPayloadKeepsToolDetails(t *testing.T) {
	payload := eventPayload(agentruntime.Event{
		Kind:      agentruntime.EventToolStarted,
		Tool:      "calculator",
		CallID:    "call-1",
		Arguments: `{"a":2}`,
		Result:    "5",
	})
	for key, want := range map[string]string{"tool": "calculator", "call_id": "call-1", "arguments": `{"a":2}`, "result": "5"} {
		if payload[key] != want {
			t.Fatalf("payload[%q]=%v want %q", key, payload[key], want)
		}
	}
}

func consumeQueuedRun(t *testing.T, handler http.Handler, user, sessionID, input string) (conversation.RunRecord, string) {
	t.Helper()
	path := fmt.Sprintf("/v1/users/%s/sessions/%s/runs", user, sessionID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"input":`+jsonString(input)+`}`)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	var record conversation.RunRecord
	if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.ID == "" {
		t.Fatal("accepted run missing id")
	}
	events := httptest.NewRecorder()
	handler.ServeHTTP(events, httptest.NewRequest("GET", path+"/"+record.ID+"/events", nil))
	if events.Code != http.StatusOK {
		t.Fatalf("events: %d %s", events.Code, events.Body.String())
	}
	body := events.Body.String()
	if !strings.Contains(body, `"kind":"completed"`) && !strings.Contains(body, "event: completed") {
		t.Fatalf("events missing completed: %s", body)
	}
	return record, body
}

func jsonString(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestHTTPConversationLifecycle(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	model := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		return testutil.Text("你好"), nil
	}}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 3, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	manager := agentruntime.NewQueueManager(ctx, store, agent, agentruntime.QueueConfig{MaxWorkers: 1, PollInterval: time.Millisecond, LeaseTTL: time.Second})
	defer manager.Close()
	handler := New(store, manager)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	base := "/v1/users/alice/sessions"
	w := request("POST", base, "")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var session conversation.Session
	if err = json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	_, events := consumeQueuedRun(t, handler, "alice", session.ID, "hi")
	if !strings.Contains(events, "你好") {
		t.Fatalf("first run events: %s", events)
	}
	_, events = consumeQueuedRun(t, handler, "alice", session.ID, "hi")
	if !strings.Contains(events, "你好") {
		t.Fatalf("second run events: %s", events)
	}
	w = request("GET", base+"/"+session.ID+"/messages?limit=1", "")
	var history struct {
		Turns []conversation.Turn `json:"turns"`
		Next  int64               `json:"next_after"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &history); err != nil || len(history.Turns) != 1 || history.Next == 0 {
		t.Fatalf("history=%s err=%v", w.Body.String(), err)
	}
	w = request("GET", fmt.Sprintf("%s/%s/messages?after=%d", base, session.ID, history.Next), "")
	if !strings.Contains(w.Body.String(), "你好") {
		t.Fatal("history pagination missed second turn")
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", base + "?limit=0", "", 400},
		{"GET", "/v1/users/bob/sessions/" + session.ID + "/messages", "", 404},
		{"POST", base + "/" + session.ID + "/runs", `{"input":" "}`, 400},
		{"POST", base + "/" + session.ID + "/runs", `{"input":"hi","extra":1}`, 400},
		{"POST", base + "/" + session.ID + "/runs", `{"input":"hi"}{}`, 400},
		{"POST", base + "/missing/runs", `{"input":"hi"}`, 404},
	} {
		w = request(tc.method, tc.path, tc.body)
		if w.Code != tc.status {
			t.Errorf("%s %s: %d want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
}

func TestGatewayInjectsTheSameUserMemoryRuntimeAsTUI(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	memories := conversation.NewMemoryLongTerm()
	defer memories.Close()
	profile, err := memories.ReplaceProfile(ctx, "alice", []conversation.MemoryDraft{{
		Kind: conversation.MemoryKindPreference, Content: "偏好先给结论", Importance: .9, Confidence: .9,
		SourceSessions: []string{session.ID}, SourceTurnIDs: []int64{1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	model := &testutil.Model{GenerateFunc: func(_ context.Context, messages []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		for _, message := range messages {
			if message.Role == schema.AgenticRoleTypeSystem && strings.Contains(message.String(), profile[0].ID) && strings.Contains(message.String(), "偏好先给结论") {
				return testutil.Text("已采用长期偏好"), nil
			}
		}
		return nil, fmt.Errorf("gateway missed user memory")
	}}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 2, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	manager := agentruntime.NewQueueManager(ctx, store, agent, agentruntime.QueueConfig{MaxWorkers: 1, PollInterval: time.Millisecond, LeaseTTL: time.Second}, memories)
	defer manager.Close()
	handler := New(store, manager)
	_, events := consumeQueuedRun(t, handler, "alice", session.ID, "回答问题")
	if !strings.Contains(events, "已采用长期偏好") {
		t.Fatalf("gateway events=%s", events)
	}
}

func TestCancelAuditsQueuedRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	memory := conversation.NewMemory()
	session, _ := memory.Create(ctx, "alice")
	started := make(chan struct{})
	model := &testutil.Model{StreamFunc: func(ctx context.Context, _ []*schema.AgenticMessage) (*schema.StreamReader[*schema.AgenticMessage], error) {
		reader, writer := schema.Pipe[*schema.AgenticMessage](1)
		go func() {
			defer writer.Close()
			writer.Send(testutil.Text("partial"), nil)
			close(started)
			<-ctx.Done()
		}()
		return reader, nil
	}}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Tools: []tool.BaseTool{calculator}, Agent: config.AgentConfig{MaxSteps: 3, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	manager := agentruntime.NewQueueManager(ctx, memory, agent, agentruntime.QueueConfig{MaxWorkers: 1, PollInterval: time.Millisecond, LeaseTTL: time.Second})
	defer manager.Close()
	handler := New(memory, manager)
	submit := httptest.NewRecorder()
	handler.ServeHTTP(submit, httptest.NewRequest("POST", "/v1/users/alice/sessions/"+session.ID+"/runs", strings.NewReader(`{"input":"hi"}`)))
	if submit.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", submit.Code, submit.Body.String())
	}
	var record conversation.RunRecord
	if err := json.Unmarshal(submit.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("queued run did not start")
	}
	cancelResp := httptest.NewRecorder()
	handler.ServeHTTP(cancelResp, httptest.NewRequest("DELETE", "/v1/users/alice/sessions/"+session.ID+"/runs/"+record.ID, nil))
	if cancelResp.Code != http.StatusOK && cancelResp.Code != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", cancelResp.Code, cancelResp.Body.String())
	}
	events := httptest.NewRecorder()
	handler.ServeHTTP(events, httptest.NewRequest("GET", "/v1/users/alice/sessions/"+session.ID+"/runs/"+record.ID+"/events", nil))
	deadline := time.Now().Add(5 * time.Second)
	var turns []conversation.Turn
	for time.Now().Before(deadline) {
		var err error
		turns, err = memory.History(ctx, "alice", session.ID, 0, 10)
		if err == nil && len(turns) == 1 && turns[0].Status == "canceled" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(turns) != 1 || turns[0].Status != "canceled" {
		t.Fatalf("cancel audit=%+v events=%s", turns, events.Body.String())
	}
	lease, err := memory.Begin(ctx, "alice", session.ID)
	if err != nil {
		t.Fatalf("cancel left session locked: %v", err)
	}
	lease.Close()
}
