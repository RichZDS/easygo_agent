package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"easygo-agent/internal/conversation"
	"easygo-agent/internal/task"
)

func TestTaskRoutesOwnerScopeEventsAndResume(t *testing.T) {
	ctx := context.Background()
	conversations := conversation.NewMemory()
	session, _ := conversations.Create(ctx, "alice")
	store := task.NewMemory()
	owner := task.Owner{User: "alice", Session: session.ID, Run: "parent"}
	created, err := store.Create(ctx, owner, task.Brief{Role: "analyst", Goal: "analyze", Acceptance: []string{"evidence"}, Key: "one"})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(conversations, nil, &task.Service{Store: store})
	base := "/v1/users/alice/sessions/" + session.ID + "/tasks"
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	if w := request("GET", base, ""); w.Code != 200 || !strings.Contains(w.Body.String(), created.ID) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if w := request("GET", strings.Replace(base, "alice", "bob", 1)+"/"+created.ID, ""); w.Code != 404 {
		t.Fatalf("cross-user: %d", w.Code)
	}
	if w := request("POST", base+"/"+created.ID+"/resume", `{}`); w.Code != 409 {
		t.Fatalf("active resume: %d %s", w.Code, w.Body)
	}
	if w := request("DELETE", base+"/"+created.ID, ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	w := request("GET", base+"/"+created.ID+"/events?after=1", "")
	var out struct {
		Events []task.Event `json:"events"`
		After  int64        `json:"next_after"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 1 || out.Events[0].Seq != 2 || out.After != 2 {
		t.Fatalf("event replay %+v", out)
	}
	if w = request("POST", base+"/"+created.ID+"/resume", `{"instructions":"new constraint"}`); w.Code != 202 || !strings.Contains(w.Body.String(), `"version":2`) {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	if w = request("GET", base+"/"+created.ID+"/events?after=-1", ""); w.Code != 400 {
		t.Fatal("negative cursor accepted")
	}
}
