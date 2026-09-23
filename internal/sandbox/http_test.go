package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestOperatorCapacityReportsFailureThenClearsOnActive(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	engine.startFn = func(context.Context, string) error { return errors.New("injected start failure") }
	handler := newTestHandler(t, manager)
	application := mustApply(t, manager, "session-a")
	identity := Identity{SessionID: "session-a", RunID: "run-a"}

	response := doSandboxRequest(t, handler, http.MethodPost, "/v1/applications/"+application.ID+"/create", `{}`, "session-a", "run-a")
	assertHTTPError(t, response, http.StatusInternalServerError, CodeInternal)

	failed := getOperatorCapacity(t, handler)
	failedRow := operatorRow(t, failed, application.ID)
	if failedRow.LastFailureCode != CodeInternal || failedRow.State == "" {
		t.Fatalf("operator row after start failure: %+v", failedRow)
	}
	if failed.Running != runningCount(t, manager) || failed.Starting != failed.ByState[StateStarting] || failed.Waiters != waiterCount(t, manager) {
		t.Fatalf("capacity gauges running=%d starting=%d waiters=%d by_state=%v manager running=%d waiters=%d", failed.Running, failed.Starting, failed.Waiters, failed.ByState, runningCount(t, manager), waiterCount(t, manager))
	}
	if len(failed.ByState) != 9 || failed.ByState[StateDestroyed] != 0 || failed.MaxRunning != manager.cfg.MaxRunning || failed.MaxApplications != manager.cfg.MaxApplications || failed.MaxStarting != manager.cfg.MaxStarting {
		t.Fatalf("capacity limits/by_state: %+v", failed)
	}
	assertNoOperatorSecrets(t, capacityBody(t, handler))
	persisted := persistedApplication(t, manager, application.ID)
	if persisted.LastFailureCode != CodeInternal {
		t.Fatalf("persisted failure code=%q", persisted.LastFailureCode)
	}

	statusResponse := doSandboxRequest(t, handler, http.MethodGet, "/v1/applications/"+application.ID, "", "session-a", "run-a")
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status after failure: %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	assertAgentStatusOmitsFailure(t, statusResponse.Body.Bytes())
	status, err := manager.Status(context.Background(), identity, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	encodedStatus, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	assertAgentStatusOmitsFailure(t, encodedStatus)

	engine.startFn = nil
	response = doSandboxRequest(t, handler, http.MethodPost, "/v1/applications/"+application.ID+"/create", `{}`, "session-a", "run-a")
	if response.Code != http.StatusOK {
		t.Fatalf("retry create status=%d body=%s", response.Code, response.Body.String())
	}
	active := getOperatorCapacity(t, handler)
	activeRow := operatorRow(t, active, application.ID)
	if activeRow.State != StateActive || activeRow.LastFailureCode != "" {
		t.Fatalf("operator row after active: %+v", activeRow)
	}
	if active.Running != runningCount(t, manager) || active.ByState[StateActive] != 1 {
		t.Fatalf("active capacity: %+v running=%d", active, runningCount(t, manager))
	}
	persisted = persistedApplication(t, manager, application.ID)
	if persisted.LastFailureCode != "" || persisted.State != StateActive {
		t.Fatalf("persisted after active: state=%s code=%q", persisted.State, persisted.LastFailureCode)
	}
	assertAgentStatusOmitsFailure(t, doSandboxRequest(t, handler, http.MethodGet, "/v1/applications/"+application.ID, "", "session-a", "run-a").Body.Bytes())

	manager.mu.Lock()
	current := manager.applications[application.ID]
	current.LastFailureCode = CodeInternal
	persistErr := manager.persistLocked(current)
	manager.mu.Unlock()
	if persistErr != nil {
		t.Fatal(persistErr)
	}
	response = doSandboxRequest(t, handler, http.MethodPost, "/v1/applications/"+application.ID+"/exec", `{"command":"true","timeout_seconds":5}`, "session-a", "run-a")
	if response.Code != http.StatusOK {
		t.Fatalf("exec status=%d body=%s", response.Code, response.Body.String())
	}
	warm := operatorRow(t, getOperatorCapacity(t, handler), application.ID)
	if warm.State != StateWarmIdle || warm.LastFailureCode != "" {
		t.Fatalf("operator row after warm idle: %+v", warm)
	}
	persisted = persistedApplication(t, manager, application.ID)
	if persisted.State != StateWarmIdle || persisted.LastFailureCode != "" {
		t.Fatalf("persisted warm idle: state=%s code=%q", persisted.State, persisted.LastFailureCode)
	}
}

func TestOperatorCapacityRecordsStopFailureUntilNextActive(t *testing.T) {
	manager, engine, _ := newTestManager(t, testConfig())
	handler := newTestHandler(t, manager)
	application := mustApply(t, manager, "session-a")
	response := doSandboxRequest(t, handler, http.MethodPost, "/v1/applications/"+application.ID+"/create", `{}`, "session-a", "run-a")
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	engine.setStopError(errors.New("injected stop failure"))
	response = doSandboxRequest(t, handler, http.MethodPost, "/v1/applications/"+application.ID+"/release", `{}`, "session-a", "run-a")
	assertHTTPError(t, response, http.StatusInternalServerError, CodeInternal)

	failed := getOperatorCapacity(t, handler)
	failedRow := operatorRow(t, failed, application.ID)
	if failedRow.State != StateHibernating || failedRow.LastFailureCode != CodeInternal {
		t.Fatalf("operator row after stop failure: %+v", failedRow)
	}
	persisted := persistedApplication(t, manager, application.ID)
	if persisted.LastFailureCode != CodeInternal || persisted.State != StateHibernating {
		t.Fatalf("persisted stop failure: state=%s code=%q", persisted.State, persisted.LastFailureCode)
	}

	engine.setStopError(nil)
	if err := manager.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	hibernated := operatorRow(t, getOperatorCapacity(t, handler), application.ID)
	if hibernated.State != StateHibernated || hibernated.LastFailureCode != CodeInternal {
		t.Fatalf("successful hibernate cleared stop failure: %+v", hibernated)
	}
	response = doSandboxRequest(t, handler, http.MethodPost, "/v1/applications/"+application.ID+"/create", `{}`, "session-a", "run-a")
	if response.Code != http.StatusOK {
		t.Fatalf("recreate status=%d body=%s", response.Code, response.Body.String())
	}
	active := operatorRow(t, getOperatorCapacity(t, handler), application.ID)
	if active.State != StateActive || active.LastFailureCode != "" {
		t.Fatalf("operator row after stop recovery: %+v", active)
	}
}

func TestOperatorCapacityRejectsIdentityHeadersAndMissingBearer(t *testing.T) {
	manager, _, _ := newTestManager(t, testConfig())
	handler := newTestHandler(t, manager)
	_ = mustApply(t, manager, "session-a")

	request := httptest.NewRequest(http.MethodGet, operatorCapacityPath, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertHTTPError(t, response, http.StatusUnauthorized, CodeUnauthorized)

	request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz status=%d body=%s", response.Code, response.Body.String())
	}

	for _, header := range []string{sessionHeader, runHeader} {
		request = httptest.NewRequest(http.MethodGet, operatorCapacityPath, nil)
		request.Header.Set("Authorization", "Bearer "+testHTTPToken)
		request.Header.Set(header, "present")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertHTTPError(t, response, http.StatusBadRequest, CodeInvalidRequest)
	}

	request = httptest.NewRequest(http.MethodPost, operatorCapacityPath, nil)
	request.Header.Set("Authorization", "Bearer "+testHTTPToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertHTTPError(t, response, http.StatusMethodNotAllowed, CodeInvalidRequest)

	capacity := getOperatorCapacity(t, handler)
	if capacity.Waiters != 0 || capacity.ByState[StateApplied] != 1 || len(capacity.Applications) != 1 {
		t.Fatalf("anonymous operator read changed capacity: %+v", capacity)
	}
	assertNoOperatorSecrets(t, capacityBody(t, handler))
}

func newTestHandler(t *testing.T, manager *Manager) http.Handler {
	t.Helper()
	handler, err := NewHTTPHandler(testHTTPToken, manager)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func getOperatorCapacity(t *testing.T, handler http.Handler) OperatorCapacity {
	t.Helper()
	var capacity OperatorCapacity
	if err := json.Unmarshal(capacityBody(t, handler), &capacity); err != nil {
		t.Fatal(err)
	}
	return capacity
}

func capacityBody(t *testing.T, handler http.Handler) []byte {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, operatorCapacityPath, nil)
	request.Header.Set("Authorization", "Bearer "+testHTTPToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("capacity status=%d body=%s", response.Code, response.Body.String())
	}
	return response.Body.Bytes()
}

func operatorRow(t *testing.T, capacity OperatorCapacity, applicationID string) OperatorApplication {
	t.Helper()
	for _, row := range capacity.Applications {
		if row.ID == applicationID {
			return row
		}
	}
	t.Fatalf("application %s missing from operator capacity: %+v", applicationID, capacity.Applications)
	return OperatorApplication{}
}

func persistedApplication(t *testing.T, manager *Manager, applicationID string) Application {
	t.Helper()
	records, err := manager.store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.ID == applicationID {
			return record
		}
	}
	t.Fatalf("application %s missing from store", applicationID)
	return Application{}
}

func runningCount(t *testing.T, manager *Manager) int {
	t.Helper()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.runningCountLocked()
}

func waiterCount(t *testing.T, manager *Manager) int {
	t.Helper()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return len(manager.waiters)
}

func assertAgentStatusOmitsFailure(t *testing.T, body []byte) {
	t.Helper()
	text := string(body)
	if strings.Contains(text, "last_failure_code") || strings.Contains(text, "session_id") {
		t.Fatalf("agent status leaked operator fields: %s", text)
	}
}

func assertNoOperatorSecrets(t *testing.T, body []byte) {
	t.Helper()
	text := string(body)
	for _, forbidden := range []string{"session_id", "volume_name", "container_id", "seen_run_ids", "session-a", testHTTPToken, "docker.sock", "/tmp/test-state.db"} {
		if forbidden != "" && strings.Contains(text, forbidden) {
			t.Fatalf("operator capacity leaked %q in %s", forbidden, text)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	rows, ok := payload["applications"].([]any)
	if !ok {
		t.Fatalf("applications=%T", payload["applications"])
	}
	allowed := map[string]bool{"id": true, "state": true, "last_failure_code": true, "idle_expires_at": true, "hard_expires_at": true}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("row type %T", raw)
		}
		for key := range row {
			if !allowed[key] {
				t.Fatalf("operator application field %q in %s", key, text)
			}
		}
		if _, ok := row["last_failure_code"]; !ok {
			t.Fatalf("last_failure_code missing from %s", text)
		}
	}
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
