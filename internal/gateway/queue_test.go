package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/config"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"

	deepagent "easygo-agent/internal/agent/deepagent.go"
	"github.com/cloudwego/eino/schema"
)

func TestAsyncRunProtocol(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	model := &testutil.Model{GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
		return testutil.Text("queued answer"), nil
	}}
	agent, err := deepagent.New(ctx, deepagent.Config{ChatModel: model, Agent: config.AgentConfig{MaxSteps: 3, ContextTokens: 24000}})
	if err != nil {
		t.Fatal(err)
	}
	manager := agentruntime.NewQueueManager(ctx, store, agent, agentruntime.QueueConfig{MaxWorkers: 1, PollInterval: time.Millisecond, LeaseTTL: time.Second})
	defer manager.Close()
	handler := New(store, agent, manager)

	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/users/alice/sessions", nil))
	if create.Code != http.StatusCreated {
		t.Fatalf("create session: %d %s", create.Code, create.Body.String())
	}
	var session conversation.Session
	if err := json.Unmarshal(create.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/users/alice/sessions/"+session.ID+"/runs", strings.NewReader(`{"input":"hello"}`))
	request.Header.Set("Idempotency-Key", "k1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", response.Code, response.Body.String())
	}
	var submitted conversation.RunRecord
	if err := json.Unmarshal(response.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.Status != conversation.RunQueued || submitted.ID == "" {
		t.Fatalf("submitted=%+v", submitted)
	}

	var final conversation.RunRecord
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		get := httptest.NewRecorder()
		handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/users/alice/sessions/"+session.ID+"/runs/"+submitted.ID, nil))
		if get.Code != http.StatusOK {
			t.Fatalf("get: %d %s", get.Code, get.Body.String())
		}
		if err := json.Unmarshal(get.Body.Bytes(), &final); err != nil {
			t.Fatal(err)
		}
		if final.Status == conversation.RunCompleted || final.Status == conversation.RunFailed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if final.Status != conversation.RunCompleted || final.ResultText != "queued answer" {
		t.Fatalf("final=%+v", final)
	}

	retry := httptest.NewRecorder()
	retryRequest := httptest.NewRequest(http.MethodPost, "/v1/users/alice/sessions/"+session.ID+"/runs", strings.NewReader(`{"input":"hello"}`))
	retryRequest.Header.Set("Idempotency-Key", "k1")
	handler.ServeHTTP(retry, retryRequest)
	if retry.Code != http.StatusAccepted || !strings.Contains(retry.Body.String(), submitted.ID) {
		t.Fatalf("idempotent retry: %d %s", retry.Code, retry.Body.String())
	}
}
