package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testHTTPToken = "test-http-controller-token-0123456789abcdef"

func TestHTTPContractRequiresBearerAndTrustedIdentity(t *testing.T) {
	manager, _, _ := newTestManager(t, testConfig())
	handler, err := NewHTTPHandler(testHTTPToken, manager)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/applications", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertHTTPError(t, response, http.StatusUnauthorized, CodeUnauthorized)

	request = httptest.NewRequest(http.MethodPost, "/v1/applications", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testHTTPToken)
	request.Header.Set(sessionHeader, "session-a")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertHTTPError(t, response, http.StatusBadRequest, CodeInvalidRequest)
}

func TestHTTPApplicationLifecycleUsesSnakeCaseContract(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	engine.execFn = func(_ context.Context, _ string, request EngineExecRequest) (EngineExecResult, error) {
		if request.User == "0:0" {
			return EngineExecResult{}, nil
		}
		return EngineExecResult{ExitCode: 0, Stdout: "ok\n"}, nil
	}
	handler, err := NewHTTPHandler(testHTTPToken, manager)
	if err != nil {
		t.Fatal(err)
	}
	response := doSandboxRequest(t, handler, http.MethodPost, "/v1/applications", `{}`, "session-a", "run-a")
	if response.Code != http.StatusCreated {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.String())
	}
	var applied ApplyResult
	if err := json.Unmarshal(response.Body.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if !applied.Created || applied.Application.WorkspaceBytes != 0 || applied.Application.HardExpiresAt == "" {
		t.Fatalf("apply=%+v", applied)
	}
	applicationID := applied.Application.ID
	response = doSandboxRequest(t, handler, http.MethodPost, "/v1/applications/"+applicationID+"/create", `{}`, "session-a", "run-a")
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	response = doSandboxRequest(t, handler, http.MethodPost, "/v1/applications/"+applicationID+"/exec", `{"command":"true","timeout_seconds":5}`, "session-a", "run-a")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"exit_code":0`) || !strings.Contains(response.Body.String(), `"duration_ms":`) {
		t.Fatalf("exec status=%d body=%s", response.Code, response.Body.String())
	}
	response = doSandboxRequest(t, handler, http.MethodPost, "/v1/applications/"+applicationID+"/release", `{}`, "session-a", "run-a")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"hibernated"`) {
		t.Fatalf("release status=%d body=%s", response.Code, response.Body.String())
	}
	response = doSandboxRequest(t, handler, http.MethodDelete, "/v1/applications/"+applicationID, "", "session-a", "run-a")
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("destroy status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPRejectsUnknownJSONFieldsAndHidesCrossSessionDestroy(t *testing.T) {
	manager, _, _ := newTestManager(t, testConfig())
	handler, err := NewHTTPHandler(testHTTPToken, manager)
	if err != nil {
		t.Fatal(err)
	}
	application := mustApply(t, manager, "owner")
	response := doSandboxRequest(t, handler, http.MethodPost, "/v1/applications/"+application.ID+"/exec", `{"command":"true","timeout_ms":1}`, "owner", "owner-run")
	assertHTTPError(t, response, http.StatusBadRequest, CodeInvalidRequest)
	response = doSandboxRequest(t, handler, http.MethodDelete, "/v1/applications/"+application.ID, "", "attacker", "attacker-run")
	if response.Code != http.StatusNoContent {
		t.Fatalf("cross-session destroy status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := manager.Status(context.Background(), Identity{SessionID: "owner", RunID: "owner-run"}, application.ID); err != nil {
		t.Fatalf("owner application was destroyed: %v", err)
	}
}

func doSandboxRequest(t *testing.T, handler http.Handler, method, target, body, sessionID, runID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+testHTTPToken)
	request.Header.Set(sessionHeader, sessionID)
	request.Header.Set(runHeader, runID)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertHTTPError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != code {
		t.Fatalf("error code=%q body=%s", envelope.Error.Code, response.Body.String())
	}
}
