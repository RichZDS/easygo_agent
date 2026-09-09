package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	deepagent "easygo-agent/internal/agent/deepagent.go"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"
	"easygo-agent/internal/tools"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestHTTPConversationLifecycle(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	model := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		return testutil.Text("你好"), nil
	}}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, model, []tool.BaseTool{calculator}, config.AgentConfig{MaxSteps: 3, ContextTokens: 24000})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, agent)
	request := func(method, path, body, accept string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	base := "/v1/users/alice/sessions"
	w := request("POST", base, "", "")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var session conversation.Session
	if err = json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	for _, accept := range []string{"application/json", "text/event-stream"} {
		w = request("POST", base+"/"+session.ID+"/runs", `{"input":"hi"}`, accept)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "你好") || !strings.Contains(w.Body.String(), "completed") {
			t.Fatalf("run (%s): %d %s", accept, w.Code, w.Body.String())
		}
	}
	w = request("GET", base+"/"+session.ID+"/messages?limit=1", "", "")
	var history struct {
		Turns []conversation.Turn `json:"turns"`
		Next  int64               `json:"next_after"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &history); err != nil || len(history.Turns) != 1 || history.Next == 0 {
		t.Fatalf("history=%s err=%v", w.Body.String(), err)
	}
	w = request("GET", fmt.Sprintf("%s/%s/messages?after=%d", base, session.ID, history.Next), "", "")
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
		w = request(tc.method, tc.path, tc.body, "")
		if w.Code != tc.status {
			t.Errorf("%s %s: %d want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
	lease, _ := store.Begin(ctx, "alice", session.ID)
	w = request("POST", base+"/"+session.ID+"/runs", `{"input":"hi"}`, "")
	lease.Close()
	if w.Code != http.StatusConflict {
		t.Fatalf("busy=%d", w.Code)
	}
}

func TestGatewayInjectsTheSameUserMemoryRuntimeAsTUI(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.ReplaceProfile(ctx, "alice", []conversation.MemoryDraft{{
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
	agent, err := deepagent.New(ctx, model, []tool.BaseTool{calculator}, config.AgentConfig{MaxSteps: 2, ContextTokens: 24000})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, agent, store)
	request := httptest.NewRequest("POST", "/v1/users/alice/sessions/"+session.ID+"/runs", strings.NewReader(`{"input":"回答问题"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "已采用长期偏好") {
		t.Fatalf("gateway response=%d %s", response.Code, response.Body.String())
	}
}

type observedStore struct {
	conversation.Store
	committed chan string
}
type observedLease struct {
	conversation.Lease
	committed chan string
}

func (s observedStore) Begin(ctx context.Context, user, id string) (conversation.Lease, error) {
	l, err := s.Store.Begin(ctx, user, id)
	if err != nil {
		return nil, err
	}
	return observedLease{l, s.committed}, nil
}
func (l observedLease) Commit(ctx context.Context, messages []*schema.AgenticMessage, turn conversation.Turn) error {
	err := l.Lease.Commit(ctx, messages, turn)
	if err == nil {
		l.committed <- turn.Status
	}
	return err
}

func TestDisconnectCancelsAndAuditsRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	memory := conversation.NewMemory()
	session, _ := memory.Create(ctx, "alice")
	committed := make(chan string, 1)
	model := &testutil.Model{StreamFunc: func(ctx context.Context, _ []*schema.AgenticMessage) (*schema.StreamReader[*schema.AgenticMessage], error) {
		reader, writer := schema.Pipe[*schema.AgenticMessage](1)
		go func() { defer writer.Close(); writer.Send(testutil.Text("partial"), nil); <-ctx.Done() }()
		return reader, nil
	}}
	calculator, _ := tools.NewCalculator()
	agent, err := deepagent.New(ctx, model, []tool.BaseTool{calculator}, config.AgentConfig{MaxSteps: 3, ContextTokens: 24000})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	handler := New(observedStore{memory, committed}, agent)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/users/alice/sessions/"+session.ID+"/runs", strings.NewReader(`{"input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case status := <-committed:
		if status != "canceled" {
			t.Fatalf("disconnect status=%s", status)
		}
	case <-ctx.Done():
		t.Fatal("disconnect did not release run")
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("HTTP cleanup did not finish")
	}
	lease, err := memory.Begin(ctx, "alice", session.ID)
	if err != nil {
		t.Fatalf("disconnect left session locked: %v", err)
	}
	lease.Close()
	turns, err := memory.History(ctx, "alice", session.ID, 0, 10)
	if err != nil || len(turns) != 1 || turns[0].Status != "canceled" {
		t.Fatalf("disconnect audit=%+v err=%v", turns, err)
	}
}
