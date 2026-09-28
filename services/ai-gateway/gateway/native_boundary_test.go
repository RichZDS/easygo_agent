package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeBoundaryRouteAndAccounting(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int64
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Error(err)
				}
				if r.Method != "POST" || r.URL.Path != "/operator-endpoint" || body["model"] != "operator-model" || body["temperature"] != float64(0) {
					t.Errorf("route/model/parameters changed: %s %s %s", r.Method, r.URL, raw)
				}
				if body["tools"] == nil {
					t.Error("native tools lost")
				}
				if r.Header.Get("X-Operator") != "configured" || r.Header.Get("X-Child") != "" {
					t.Error("caller headers overrode operator")
				}
				if protocol == "anthropic" {
					if r.Header.Get("X-Api-Key") != "dummy-provider-key" || r.Header.Get("Anthropic-Version") == "" {
						t.Error("provider auth missing")
					}
				} else if r.Header.Get("Authorization") != "Bearer dummy-provider-key" {
					t.Error("provider auth missing")
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				fmt.Fprint(w, responseFixture(protocol))
			}))
			defer provider.Close()
			var observations []Observation
			g, err := New(Config{Models: map[string]Model{"selected": {Protocol: protocol, Endpoint: provider.URL + "/operator-endpoint", Model: "operator-model", APIKey: "dummy-provider-key", Headers: map[string]string{"X-Operator": "configured"}, Parameters: map[string]json.RawMessage{"temperature": json.RawMessage(`0`)}, Price: &Pricing{Currency: "USD", InputPerMillion: 2, OutputPerMillion: 10, CacheReadPerMillion: 1}}}, Observer: func(o Observation) { observations = append(observations, o) }})
			if err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(`{"model":"child-model","temperature":1,"tools":[{"type":"framework-special"}],"url":"http://127.0.0.1:1/forbidden","headers":{"X-Child":"forged","Authorization":"Bearer child-key"}}`)
			out, err := g.Native(context.Background(), "selected", protocol, "native-request", raw)
			if err != nil || string(out.Body) != responseFixture(protocol) {
				t.Fatalf("native roundtrip: %v %+v", err, out)
			}
			if len(observations) != 1 {
				t.Fatalf("expected one observation, got %d", len(observations))
			}
			o := observations[0]
			if o.RequestID != "native-request" || o.Model != "selected" || !o.Usage.Known || o.Usage.InputTokens != 20 || o.Usage.OutputTokens != 4 || !o.Cost.Known || o.Cost.Amount != 0.000075 || o.HasFirstDelta {
				t.Fatalf("accounting: %+v", observations)
			}
			_, err = g.Native(context.Background(), "selected", "unsupported", "mismatch", raw)
			requireCode(t, err, "unsupported_capability")
			_, err = g.Native(context.Background(), "missing", protocol, "missing", raw)
			requireCode(t, err, "unknown_model")
			for _, invalid := range []string{`null`, `[]`, `{"model":"a","model":"b"}`} {
				_, err = g.Native(context.Background(), "selected", protocol, "bad", json.RawMessage(invalid))
				requireCode(t, err, "invalid_request")
			}
			if calls.Load() != 1 {
				t.Fatalf("invalid selection reached provider: %d", calls.Load())
			}
		})
	}
}

func TestNativeBoundaryUnknownUsageIsNotFree(t *testing.T) {
	for _, body := range []string{`{"error":null,"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`, `{"status":"completed","output":[{"type":"custom_tool_call","call_id":"call-native","name":"apply_patch","input":"native text"}]}`} {
		t.Run(body, func(t *testing.T) {
			p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}))
			defer p.Close()
			var obs Observation
			g := newTestGateway(t, "responses", p.URL, func(o Observation) { obs = o })
			out, err := g.Native(context.Background(), "test", "responses", "unknown", json.RawMessage(`{}`))
			if err != nil || string(out.Body) != body {
				t.Fatalf("opaque native result: %v", err)
			}
			if obs.Usage.Known || obs.Cost.Known || obs.HasFirstDelta {
				t.Fatalf("unknown usage was presented as priced/free: %+v", obs)
			}
		})
	}
}

func TestNativeBoundaryBufferedSSEAndCancel(t *testing.T) {
	for _, mode := range []string{"complete", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			sent := make(chan struct{})
			release := make(chan struct{})
			closed := make(chan struct{})
			p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"pending\"}\n\n")
				w.(http.Flusher).Flush()
				close(sent)
				select {
				case <-release:
					fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", responseFixture("responses"))
				case <-r.Context().Done():
					close(closed)
				}
			}))
			defer p.Close()
			defer close(release)
			observed := make(chan Observation, 1)
			g := newTestGateway(t, "responses", p.URL, func(o Observation) { observed <- o })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := g.Native(ctx, "test", "responses", "buffer", json.RawMessage(`{"stream":true}`))
				result <- err
			}()
			select {
			case <-sent:
			case <-time.After(3 * time.Second):
				t.Fatal("provider not reached")
			}
			select {
			case err := <-result:
				t.Fatalf("SSE returned before upstream completion: %v", err)
			case <-time.After(75 * time.Millisecond):
			}
			if mode == "cancel" {
				cancel()
				select {
				case <-closed:
				case <-time.After(3 * time.Second):
					t.Fatal("cancel did not close provider request")
				}
			} else {
				release <- struct{}{}
			}
			select {
			case err := <-result:
				if (mode == "cancel") != (err != nil) {
					t.Fatalf("mode %s error %v", mode, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("native hung")
			}
			o := <-observed
			if o.HasFirstDelta {
				t.Fatal("buffered SSE claimed live first delta")
			}
		})
	}
}

func TestNativeBoundaryLimitsAndSanitizedHTTPError(t *testing.T) {
	for _, mode := range []string{"oversize", "truncated-http", "error", "unknown-type"} {
		t.Run(mode, func(t *testing.T) {
			p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch mode {
				case "oversize":
					fmt.Fprint(w, strings.Repeat("x", 2049))
				case "truncated-http":
					w.Header().Set("Content-Length", "200")
					fmt.Fprint(w, `{"short":true}`)
				case "error":
					w.WriteHeader(401)
					fmt.Fprint(w, "dummy-provider-private-detail")
				case "unknown-type":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, "private error")
				}
			}))
			defer p.Close()
			g, err := New(Config{Models: map[string]Model{"a": {Protocol: "responses", Endpoint: p.URL, Model: "upstream"}}, MaxResponseBytes: 2048})
			if err != nil {
				t.Fatal(err)
			}
			out, err := g.Native(context.Background(), "a", "responses", "limits", json.RawMessage(`{}`))
			if err == nil || len(out.Body) != 0 {
				t.Fatalf("accepted bad response mode %s: %v", mode, err)
			}
			if strings.Contains(err.Error(), "dummy-provider-private-detail") {
				t.Fatal("provider error leaked")
			}
		})
	}
}

// Explicit malformed/truncated/error responses are not opaque framework tool
// extensions. They must not become successful native results containing secrets.
func TestNativeBoundaryRejectsInvalidProviderSuccess(t *testing.T) {
	for _, tc := range []struct{ name, contentType, body string }{
		{"malformed-json", "application/json", `{"status":"completed"`},
		{"truncated-sse", "text/event-stream", "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial\"}\n\n"},
		{"error-sse", "text/event-stream", "data: {\"type\":\"error\",\"error\":{\"message\":\"dummy-provider-private-detail\"}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				fmt.Fprint(w, tc.body)
			}))
			defer p.Close()
			g := newTestGateway(t, "responses", p.URL, nil)
			out, err := g.Native(context.Background(), "test", "responses", "invalid", json.RawMessage(`{}`))
			if err == nil {
				t.Errorf("invalid provider response accepted as successful NativeResponse (%s)", tc.name)
			}
			if strings.Contains(string(out.Body), "dummy-provider-private-detail") {
				t.Error("raw provider error detail exposed to caller")
			}
		})
	}
}

func TestNativeNullableErrorSuccess(t *testing.T) {
	for _, stream := range []bool{false, true} {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := `{"status":"completed","error":null,"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
			} else {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}
		}))
		g := newTestGateway(t, "responses", provider.URL, nil)
		out, err := g.Native(context.Background(), "test", "responses", "nullable", json.RawMessage(`{}`))
		provider.Close()
		if err != nil || !strings.Contains(string(out.Body), `"error":null`) {
			t.Fatalf("valid nullable error rejected: %v", err)
		}
	}
}
