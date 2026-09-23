package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"easygo-agent/internal/conversation"

	"github.com/google/uuid"
)

func TestRunPhasesRejectsMismatchedUser(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	secret := "private prompt body"
	run, err := store.Enqueue(ctx, "alice", session.ID, secret, "")
	if err != nil {
		t.Fatal(err)
	}
	execution := uuid.NewString()
	duration := 1.25
	for _, phase := range []conversation.RunPhase{
		{RunID: run.ID, ExecutionID: execution, Sequence: 1, SpanID: 1, Phase: "run", Name: "agent", Event: "started"},
		{RunID: run.ID, ExecutionID: execution, Sequence: 2, SpanID: 1, Phase: "run", Name: "agent", Event: "finished", Status: "completed", DurationMS: &duration},
	} {
		if err = store.AppendRunPhase(ctx, phase); err != nil {
			t.Fatal(err)
		}
	}
	handler := New(store, nil)
	path := "/v1/users/alice/sessions/" + session.ID + "/runs/" + run.ID + "/phases?after=0"
	owner := httptest.NewRecorder()
	handler.ServeHTTP(owner, httptest.NewRequest(http.MethodGet, path, nil))
	if owner.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", owner.Code, owner.Body.String())
	}
	if strings.Contains(owner.Body.String(), secret) {
		t.Fatalf("response leaked the prompt: %s", owner.Body.String())
	}
	var page conversation.RunPhasePage
	if err = json.Unmarshal(owner.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Phases) != 2 || page.NextAfter != 2 {
		t.Fatalf("page=%+v", page)
	}
	mismatch := httptest.NewRecorder()
	handler.ServeHTTP(mismatch, httptest.NewRequest(http.MethodGet, "/v1/users/bob/sessions/"+session.ID+"/runs/"+run.ID+"/phases?after=0", nil))
	if mismatch.Code != http.StatusNotFound {
		t.Fatalf("mismatch: %d %s", mismatch.Code, mismatch.Body.String())
	}
}
