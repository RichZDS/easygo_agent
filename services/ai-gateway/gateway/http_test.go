package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"easygo-agent/services/ai-gateway/ai"
)

func TestRemoteHandlerClient(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var o object
				json.NewDecoder(r.Body).Decode(&o)
				if o["stream"] != stream {
					t.Errorf("stream=%v", o["stream"])
				}
				if stream {
					sendEvents(w, streamFixture("responses"))
				} else {
					fmt.Fprint(w, responseFixture("responses"))
				}
			}))
			defer provider.Close()
			var observed atomic.Int32
			g := newTestGateway(t, "responses", provider.URL, func(Observation) { observed.Add(1) })
			server := httptest.NewServer(NewHandler(g, HandlerConfig{BearerToken: "gateway-secret"}))
			defer server.Close()
			client, e := NewHTTPClient(HTTPClientConfig{Endpoint: server.URL + "/v1/generate", BearerToken: "gateway-secret"})
			if e != nil {
				t.Fatal(e)
			}
			seen := 0
			var emit func(ai.Event) error
			if stream {
				emit = func(ev ai.Event) error { seen++; return nil }
			}
			out, e := client.Complete(context.Background(), canonicalRequest(), emit)
			if e != nil {
				t.Fatal(e)
			}
			if out.Model != "test" || len(out.Message.Content) != 2 || out.Usage.OutputTokens != 4 || !out.Cost.Known || observed.Load() != 1 {
				t.Fatalf("out=%+v observed=%d", out, observed.Load())
			}
			if stream && seen == 0 {
				t.Fatal("no remote delta")
			}
			for _, path := range []string{"/healthz", "/v1/models"} {
				req, _ := http.NewRequest("GET", server.URL+path, nil)
				if path != "/healthz" {
					req.Header.Set("Authorization", "Bearer gateway-secret")
				}
				resp, e := http.DefaultClient.Do(req)
				if e != nil {
					t.Fatal(e)
				}
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "upstream") {
					t.Fatalf("route=%s status=%d body=%s", path, resp.StatusCode, raw)
				}
			}
		})
	}
}
func TestHTTPMethodsAuthAndLimits(t *testing.T) {
	g, e := New(Config{Models: map[string]Model{"test": {Protocol: "responses", Model: "u", Endpoint: "http://unused.invalid"}}})
	if e != nil {
		t.Fatal(e)
	}
	h := NewHandler(g, HandlerConfig{BearerToken: "secret", MaxRequestBytes: 64})
	for _, tc := range []struct {
		method, path, token, body string
		want                      int
	}{{"GET", "/healthz", "", "", 200}, {"POST", "/healthz", "", "", 405}, {"GET", "/v1/models", "", "", 401}, {"GET", "/v1/models", "secret", "", 200}, {"GET", "/v1/generate", "secret", "", 405}, {"POST", "/v1/generate", "bad", "{}", 401}, {"POST", "/v1/generate", "secret", strings.Repeat("x", 100), 400}, {"POST", "/v1/generate", "secret", `{"model":"` + strings.Repeat("x", 100) + `"}`, 413}, {"POST", "/v1/generate", "secret", `{} {}`, 400}, {"POST", "/v1/generate", "secret", `{"unknown":1}`, 400}, {"POST", "/v1/generate", "secret", `{"model":"missing"}`, 404}, {"GET", "/missing", "secret", "", 404}} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			if w.Code == 405 && w.Header().Get("Allow") == "" {
				t.Fatal("Allow missing")
			}
		})
	}
}
func TestRemoteStreamFailureAndCancel(t *testing.T) {
	for _, kind := range []string{"truncated", "provider_error", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sendEvents(w, streamFixture("responses")[:1])
				if kind == "provider_error" {
					sendEvents(w, []string{`{"type":"error","error":{"message":"private-prompt"}}`})
				}
				if kind == "cancel" {
					<-r.Context().Done()
				}
			}))
			defer provider.Close()
			g := newTestGateway(t, "responses", provider.URL, nil)
			server := httptest.NewServer(NewHandler(g, HandlerConfig{}))
			defer server.Close()
			client, _ := NewHTTPClient(HTTPClientConfig{Endpoint: server.URL + "/v1/generate"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			seen := 0
			_, e := client.Complete(ctx, canonicalRequest(), func(ai.Event) error {
				seen++
				if kind == "cancel" {
					cancel()
				}
				return nil
			})
			code := map[string]string{"truncated": "truncated_stream", "provider_error": "upstream_error", "cancel": "canceled"}[kind]
			requireCode(t, e, code)
			if seen == 0 {
				t.Fatal("partial output not forwarded")
			}
		})
	}
}
func TestRemoteRejectsMissingFinalResponse(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprint(named), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if named {
					fmt.Fprint(w, "event: delta\n")
				}
				fmt.Fprint(w, "data: {\"type\":\"text_delta\",\"delta\":\"part\"}\n\n")
			}))
			defer s.Close()
			c, _ := NewHTTPClient(HTTPClientConfig{Endpoint: s.URL})
			_, e := c.Complete(context.Background(), canonicalRequest(), func(ai.Event) error { return nil })
			code := "invalid_response"
			if named {
				code = "truncated_stream"
			}
			requireCode(t, e, code)
		})
	}
}

func TestRemoteSanitizesErrors(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"code":"unauthorized","message":"secret-password"}}`)
	}))
	defer s.Close()
	c, _ := NewHTTPClient(HTTPClientConfig{Endpoint: s.URL})
	_, e := c.Complete(context.Background(), canonicalRequest(), nil)
	requireCode(t, e, "unauthorized")
	if strings.Contains(e.Error(), "secret-password") {
		t.Fatal("remote error leaked secret")
	}
}
func TestJSONHandlerDoesNotExposePartialFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"incomplete","output":[{"type":"message","content":[{"type":"output_text","text":"private partial"}]}]}`)
	}))
	defer s.Close()
	g := newTestGateway(t, "responses", s.URL, nil)
	h := NewHandler(g, HandlerConfig{})
	raw, _ := json.Marshal(GenerateRequest{Request: canonicalRequest()})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/generate", bytes.NewReader(raw)))
	if w.Code != 502 || strings.Contains(w.Body.String(), "private partial") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
