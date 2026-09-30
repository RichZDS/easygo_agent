package server

import (
	"bufio"
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

	"easygo-agent/rpc"
	"easygo-agent/rpc/rpctest"
	"easygo-agent/services/ai-gateway/gateway"
)

const response = `{"id":"out","status":"completed","output":[{"type":"reasoning","id":"reason","encrypted_content":"opaque-proof","summary":[{"type":"summary_text","text":"thinking"}]},{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`

func fixture(t *testing.T, provider http.Handler) (*http.Client, string) {
	t.Helper()
	up := httptest.NewServer(provider)
	t.Cleanup(up.Close)
	p := rpctest.NewPKI(t)
	identity := p.Issue("gateway", false)
	caller := p.Issue("caller", false)
	s, e := New(Config{ServerConfig: rpc.ServerConfig{Listen: "127.0.0.1:0", TLS: identity, Authorization: []rpc.Authorization{{ID: "loop", CertFile: caller.CertFile, Methods: []string{"health", "gateway.models", "gateway.generate"}, Namespaces: []string{"tenant-a"}}}}, FileConfig: gateway.FileConfig{Models: map[string]gateway.FileModel{"chat": {Model: gateway.Model{Protocol: "responses", Endpoint: up.URL, Model: "fixture"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	ts := rpctest.Start(t, s)
	return rpctest.Client(t, caller, identity.CertFile), ts.URL
}
func TestRPCRoundTripAndStrictParameters(t *testing.T) {
	var calls atomic.Int64
	c, url := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, response) }))
	_, out := rpctest.Call(t, c, url, "gateway.models", `{"namespace":"tenant-a"}`)
	if out.Error != nil || !strings.Contains(fmt.Sprint(out.Result), "chat") {
		t.Fatalf("models %+v", out)
	}
	status, out := rpctest.Call(t, c, url, "gateway.generate", `{"namespace":"tenant-a","request":{"model":"chat","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}}`)
	raw, _ := json.Marshal(out.Result)
	if status != 200 || out.Error != nil || !strings.Contains(string(raw), "opaque-proof") {
		t.Fatalf("provider state lost: %s %+v", raw, out)
	}
	for _, params := range []string{
		`{"namespace":"tenant-a"}`,
		`{"namespace":"tenant-a","request":null}`,
		`{"namespace":"tenant-a","request":{"model":"chat","messages":[],"extra":0}}`,
		`{"namespace":"tenant-a","request":{"model":"chat","messages":[{"role":"user","content":[{"type":"text","text":"hi","extra":0}]}]}}`,
		`{"namespace":"tenant-a","request":{"model":"chat","messages":[]},"stream":null}`,
		`{"namespace":"tenant-a","request":{"model":"chat","messages":[]},"unexpected":1}`,
	} {
		_, out = rpctest.Call(t, c, url, "gateway.generate", params)
		if out.Error == nil || out.Error.Code != -32602 {
			t.Fatalf("invalid params accepted: %+v", out)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid requests called upstream %d", calls.Load())
	}
	_, out = rpctest.Call(t, c, url, "gateway.generate", `{"namespace":"tenant-a","request":{"model":"missing"}}`)
	if out.Error == nil || out.Error.Code != -32004 {
		t.Fatalf("unknown model %+v", out)
	}
	for _, path := range []string{"/v1/models", "/v1/generate"} {
		r, e := c.Get(url + path)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Fatalf("legacy path mounted: %s", path)
		}
	}
}
func TestRPCStreamTerminalAndCancellation(t *testing.T) {
	for _, mode := range []string{"complete", "truncated", "error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			canceled := make(chan struct{})
			c, url := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n")
				w.(http.Flusher).Flush()
				switch mode {
				case "complete":
					fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
				case "error":
					fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":{\"message\":\"private-upstream-secret\"}}\n\n")
				case "cancel":
					<-r.Context().Done()
					close(canceled)
				}
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", url+"/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":"stream-id","method":"gateway.generate","params":{"namespace":"tenant-a","request":{"model":"chat","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]},"stream":true}}`))
			req.Header.Set("Content-Type", "application/json")
			resp, e := c.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			defer resp.Body.Close()
			if resp.Header.Get("Content-Type") != "text/event-stream" {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("not SSE: %s", b)
			}
			scan := bufio.NewScanner(resp.Body)
			var events []string
			dataCount := 0
			for scan.Scan() {
				line := scan.Text()
				if strings.HasPrefix(line, "event: ") {
					events = append(events, strings.TrimPrefix(line, "event: "))
				}
				if strings.HasPrefix(line, "data: ") {
					dataCount++
					raw := strings.TrimPrefix(line, "data: ")
					var v map[string]any
					if json.Unmarshal([]byte(raw), &v) != nil || v["jsonrpc"] != "2.0" {
						t.Fatalf("invalid SSE envelope %s", raw)
					}
					event := events[len(events)-1]
					if event == "delta" {
						p := v["params"].(map[string]any)
						if v["method"] != "gateway.delta" || p["id"] != "stream-id" || p["event"] == nil {
							t.Fatalf("invalid delta: %s", raw)
						}
					} else if v["id"] != "stream-id" {
						t.Fatal("terminal id mismatch")
					}
					if strings.Contains(raw, "private-upstream-secret") {
						t.Fatal("error leaked upstream body")
					}
					if mode == "cancel" {
						cancel()
						break
					}
				}
			}
			if mode == "cancel" {
				select {
				case <-canceled:
				case <-time.After(3 * time.Second):
					t.Fatal("cancellation did not reach upstream")
				}
				return
			}
			if e = scan.Err(); e != nil {
				t.Fatal(e)
			}
			terminal := "error"
			if mode == "complete" {
				terminal = "result"
			}
			if len(events) != 2 || dataCount != 2 || events[0] != "delta" || events[1] != terminal {
				t.Fatalf("events %v", events)
			}
		})
	}
}
func TestConfigRejectsBearerAndUnknown(t *testing.T) {
	for _, raw := range []string{`{"bearer_token_env":"TOKEN"}`, `{"extra":1}`, `{"listen":":1","listen":":2"}`, `{"models":{"x":{"api_key":"secret"}}}`, `{"bearer_token":"secret"}`, `{} {}`} {
		if _, e := LoadConfig(strings.NewReader(raw)); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestLoadConfigNamesUnknownField(t *testing.T) {
	_, e := LoadConfig(strings.NewReader(`{"models":{},"extra_field":1}`))
	if e == nil || !strings.Contains(e.Error(), `unknown field "extra_field"`) {
		t.Fatalf("unknown field not named: %v", e)
	}
}
