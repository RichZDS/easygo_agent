package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheckModeDoesNotLoadConfigOrDocker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/healthz" {
			t.Fatalf("request=%s %s", request.Method, request.URL.Path)
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"-healthcheck", server.URL + "/healthz", "-config", "/does/not/exist"}, func(string) (string, bool) {
		t.Fatal("healthcheck unexpectedly read environment")
		return "", false
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != "ok" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestHealthcheckModeRejectsFailureAndCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, endpoint := range []string{server.URL, "http://user:pass@127.0.0.1/healthz"} {
		if err := checkHealth(context.Background(), endpoint, &bytes.Buffer{}); err == nil {
			t.Fatalf("healthcheck %q unexpectedly succeeded", endpoint)
		}
	}
}
