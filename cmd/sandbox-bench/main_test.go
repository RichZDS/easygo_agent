package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunReportsAllLifecyclePathsAndAlwaysDestroysApplication(t *testing.T) {
	var destroyed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-EasyGo-Session-ID") == "" || r.Header.Get("X-EasyGo-Run-ID") == "" {
			t.Error("missing trusted identity headers")
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/applications":
			writeBenchJSON(w, http.StatusCreated, map[string]any{"application": map[string]any{"application_id": "app-1"}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/create"):
			writeBenchJSON(w, http.StatusOK, map[string]any{"application": map[string]any{"application_id": "app-1"}, "queue_duration_ms": 0.5})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
			writeBenchJSON(w, http.StatusOK, map[string]any{"application": map[string]any{"application_id": "app-1"}, "exit_code": 0})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/release"):
			writeBenchJSON(w, http.StatusOK, map[string]any{"application": map[string]any{"application_id": "app-1"}})
		case r.Method == http.MethodDelete:
			destroyed.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observer := &fakeBenchmarkObserver{}
	var output bytes.Buffer
	err := runWithObserverFactory(context.Background(), []string{
		"-base-url", server.URL,
		"-docker-host", "/tmp/docker.sock",
		"-namespace", "test-namespace",
		"-warmups", "0",
		"-iterations", "1",
		"-concurrency", "1",
		"-workloads", "noop",
	}, testBenchmarkEnvironment, &output, func(context.Context, string, string) (benchmarkObserver, error) {
		return observer, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != 1 {
		t.Fatalf("destroyed=%d", destroyed.Load())
	}
	if observer.removes.Load() != 1 {
		t.Fatalf("direct removes=%d", observer.removes.Load())
	}
	var report benchmarkReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output.String())
	}
	for _, phase := range []string{"queue", "create", "start", "ready", "exec", "stop", "remove"} {
		if !reportHasPhase(report, phase) {
			t.Fatalf("missing phase %q", phase)
		}
	}
	for _, path := range []string{pathActiveReuse, pathStopStart, pathContainerLossRecreate} {
		if !reportHasPath(report, path) {
			t.Fatalf("missing path %q", path)
		}
	}
	if report.SchemaVersion != 4 || len(report.Samples) == 0 || len(report.Summaries) == 0 {
		t.Fatalf("incomplete report: %+v", report)
	}
	var foundExecResources, foundMeasuredQueue, foundHostLifecycle, foundExactHTTPDuration bool
	for _, sample := range report.Samples {
		if sample.Phase == "exec" && sample.CPUTimeMS != nil && sample.PeakMemoryBytes != nil && sample.PeakPIDs != nil {
			foundExecResources = true
		}
		if sample.Phase == "queue" && sample.DurationMS != nil && *sample.DurationMS == 0.5 {
			foundMeasuredQueue = true
		}
		if sample.Phase == "create" && sample.HostCPUTimeMS != nil && sample.HostPeakRSSBytes != nil && sample.HostPeakPIDs != nil && sample.HostObservation != "" {
			foundHostLifecycle = true
		}
		if sample.Path == pathLifecycle && sample.Phase == "apply" && sample.DurationMS != nil && *sample.DurationMS == 12.5 {
			foundExactHTTPDuration = true
		}
	}
	if !foundExecResources || !foundMeasuredQueue || !foundHostLifecycle || !foundExactHTTPDuration {
		t.Fatalf("resource or missing-observation metadata absent: %+v", report.Samples)
	}
}

func TestBenchmarkLifecycleRecordsFallbackCleanupAfterFailure(t *testing.T) {
	var destroyed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/applications":
			writeBenchJSON(w, http.StatusCreated, map[string]any{"application": map[string]any{"id": "app-cleanup"}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/create"):
			writeBenchJSON(w, http.StatusOK, map[string]any{"application": map[string]any{"id": "app-cleanup"}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
			writeBenchJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "internal_error", "message": "boom"}})
		case r.Method == http.MethodDelete:
			destroyed.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &controllerClient{baseURL: server.URL, token: "test-token", http: server.Client(), engine: &fakeBenchmarkObserver{}}
	samples := client.benchmarkLifecycle(context.Background(), 1, 0, []string{"noop"})
	if destroyed.Load() != 1 {
		t.Fatalf("destroyed=%d", destroyed.Load())
	}
	if !samplesHavePhaseAndPath(samples, "destroy", pathCleanup) {
		t.Fatalf("missing cleanup destroy in %+v", samples)
	}
}

func TestParseBenchmarkConfigUsesSocketAndNamespaceEnvironment(t *testing.T) {
	cfg, err := parseBenchmarkConfig([]string{"-warmups", "0", "-iterations", "1", "-concurrency", "1", "-workloads", "noop"}, func(name string) string {
		switch name {
		case benchmarkDockerHostEnvironment:
			return "/private/tmp/docker.sock"
		case benchmarkNamespaceEnvironment:
			return "env-namespace"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DockerHost != "unix:///private/tmp/docker.sock" || cfg.Namespace != "env-namespace" {
		t.Fatalf("config=%+v", cfg)
	}
	if _, err := parseBenchmarkConfig([]string{"-docker-host", "tcp://remote.example:2375"}, func(string) string { return "" }); err == nil {
		t.Fatal("remote Docker host should be rejected")
	}
}

func writeBenchJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func testBenchmarkEnvironment(name string) string {
	if name == benchmarkTokenEnvironment {
		return "test-token"
	}
	return ""
}

func reportHasPhase(report benchmarkReport, phase string) bool {
	for _, summary := range report.Summaries {
		if summary.Phase == phase {
			return true
		}
	}
	return false
}

func reportHasPath(report benchmarkReport, path string) bool {
	for _, sample := range report.Samples {
		if sample.Path == path {
			return true
		}
	}
	return false
}

func samplesHavePhaseAndPath(samples []benchmarkSample, phase, path string) bool {
	for _, sample := range samples {
		if sample.Phase == phase && sample.Path == path {
			return true
		}
	}
	return false
}

type fakeBenchmarkObserver struct {
	removes atomic.Int32
}

func (*fakeBenchmarkObserver) ObserveHostPhase(_ context.Context, call func() error) hostObservedCall {
	return hostObservedCall{RequestError: call(), DurationMS: 12.5, Host: fakeHostObservation()}
}

func (observer *fakeBenchmarkObserver) ObserveActivation(_ context.Context, _ string, expectCreate bool, call func() (float64, error)) observedCall {
	queue, err := call()
	result := observedCall{RequestError: err}
	result.Phases = []phaseObservation{
		{Phase: "queue", DurationMS: &queue, Observation: "controller"},
		fakePhase("create", err), fakePhase("start", err), fakePhase("ready", err),
	}
	if !expectCreate {
		result.Phases[1].DurationMS = nil
		result.Phases[1].MissingReason = "not applicable"
	}
	return result
}

func (observer *fakeBenchmarkObserver) ObserveStop(_ context.Context, _ string, call func() error) observedCall {
	err := call()
	return observedCall{Phases: []phaseObservation{fakePhase("stop", err)}, RequestError: err}
}

func (observer *fakeBenchmarkObserver) ObserveExec(_ context.Context, _ string, call func() error) observedCall {
	err := call()
	cpu := 1.25
	memory := uint64(1024)
	pids := uint64(2)
	ioBytes := uint64(32)
	return observedCall{
		Phases: []phaseObservation{fakePhase("exec", err)}, RequestError: err,
		CPUTimeMS: &cpu, PeakMemory: &memory, PeakPIDs: &pids, BlockIOBytes: &ioBytes,
		StatsSamples: 2, StatsMethod: "fake cgroup samples",
	}
}

func (observer *fakeBenchmarkObserver) RemoveApplicationContainer(context.Context, string) observedCall {
	observer.removes.Add(1)
	return observedCall{Phases: []phaseObservation{fakePhase("remove", nil)}}
}

func (observer *fakeBenchmarkObserver) Close() error { return nil }

func fakePhase(phase string, err error) phaseObservation {
	value := 1.0
	result := phaseObservation{
		Phase: phase, DurationMS: &value, Observation: "fake",
		Host: fakeHostObservation(),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func fakeHostObservation() hostObservation {
	cpu := 1.0
	rss := uint64(2048)
	pids := uint64(3)
	return hostObservation{CPUTimeMS: &cpu, PeakRSSBytes: &rss, PeakPIDs: &pids, Samples: 2, Method: "fake host process samples"}
}
