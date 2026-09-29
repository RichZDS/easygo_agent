package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestCrewRetriesSameClientID(t *testing.T) {
	token := uuid.NewString()
	var mu sync.Mutex
	requests := 0
	ids := map[string]bool{}
	receiptID := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing credential")
		}
		var p struct {
			ClientID string `json:"client_id"`
		}
		if json.NewDecoder(r.Body).Decode(&p) != nil || p.ClientID == "" {
			t.Error("missing client id")
		}
		ids[p.ClientID] = true
		requests++
		if requests == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": receiptID, "sequence": 1})
	}))
	defer server.Close()
	getenv := func(k string) string {
		if k == "EASYGO_CREW_URL" {
			return server.URL + "/crew"
		}
		return token
	}
	var out, diag bytes.Buffer
	if code := run([]string{"submit", "--tests", "pass", "done"}, getenv, &out, &diag); code != 0 {
		t.Fatalf("code=%d diagnostic=%s", code, diag.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 || len(ids) != 1 || !strings.Contains(out.String(), receiptID) {
		t.Fatal("retry did not reuse identity", requests, len(ids))
	}
}
func TestCrewUnavailableAndTokenSafeErrors(t *testing.T) {
	for _, missing := range []string{"EASYGO_CREW_URL", "EASYGO_CREW_TOKEN"} {
		var out, diag bytes.Buffer
		code := run([]string{"report", "hello"}, func(k string) string {
			if k == missing {
				return ""
			}
			return "dummy"
		}, &out, &diag)
		if code != 2 || strings.TrimSpace(diag.String()) != "crew channel unavailable" {
			t.Fatal(code, diag.String())
		}
	}
	token := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, token, 409) }))
	defer server.Close()
	var out, diag bytes.Buffer
	code := run([]string{"report", "hello"}, func(k string) string {
		if k == "EASYGO_CREW_URL" {
			return server.URL
		}
		return token
	}, &out, &diag)
	if code != 1 || strings.Contains(diag.String(), token) || out.Len() != 0 {
		t.Fatal("credential leaked or rejection accepted")
	}
}
