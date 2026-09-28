package meter

import (
	"context"
	"easygo-agent/services/ai-gateway/ai"
	"easygo-agent/services/ai-gateway/gateway"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cancelAtEOF struct {
	io.Reader
	cancel context.CancelFunc
}

func (r cancelAtEOF) Read(b []byte) (int, error) {
	n, e := r.Reader.Read(b)
	if e == io.EOF {
		r.cancel()
	}
	return n, e
}
func TestReviewKnownUsageMustSurviveContentFailure(t *testing.T) {
	cases := []struct {
		name, body     string
		native, cancel bool
	}{
		{"neutral_length", `{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":32}}`, false, false},
		{"native_length", `{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":32}}`, true, false},
		{"neutral_cancel_after_complete_body", `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":32}}`, false, true},
		{"native_supported_wire_unsupported_tool", `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"tool-1","type":"custom","custom":{"name":"fixture","input":"text"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":32}}`, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wallet := newWallet(t)
			m := newMeter(t, wallet)
			defer m.Close()
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := gateway.WithNamespace(parent, "user-one")
			calls := 0
			client := &http.Client{Transport: reviewTransport(func(*http.Request) (*http.Response, error) {
				calls++
				var body io.Reader = strings.NewReader(c.body)
				if c.cancel {
					body = cancelAtEOF{body, cancel}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(body)}, nil
			})}
			g, e := gateway.New(gateway.Config{Models: map[string]gateway.Model{"chat": {Protocol: "chat_completions", Endpoint: "https://provider.invalid/generate", Model: "fixture"}}, Billing: m, BillingMaxOutputTokens: 32, HTTPClient: client})
			if e != nil {
				t.Fatal(e)
			}
			if c.native {
				_, e = g.Native(ctx, "chat", "chat_completions", c.name, json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`))
			} else {
				_, e = g.Complete(ctx, ai.Request{RequestID: c.name, Model: "chat", Messages: []ai.Message{{Role: "user", Content: []ai.Block{{Type: "text", Text: "hello"}}}}}, nil)
			}
			if calls != 1 {
				t.Fatalf("provider calls=%d", calls)
			}
			t.Logf("provider_calls=%d gateway_error=%v", calls, e)
			if e = m.Flush(context.Background()); e != nil {
				t.Fatal(e)
			}
			wallet.mu.Lock()
			p := wallet.settled[c.name]
			wallet.mu.Unlock()
			t.Logf("wallet receipt=%+v", p)
			if !p.Usage.Known || p.Usage.InputTokens != 10 || p.Usage.OutputTokens != 32 {
				t.Fatalf("explicit provider usage lost: got %+v, want known input=10 output=32", p.Usage)
			}
		})
	}
}

// One complete SSE event per transport read ensures accounting cannot rely on
// bufio prefetch having already received the final usage when content fails.
type usageChunks struct{ chunks []string }

func (r *usageChunks) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if r.chunks[0] == "" {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}
func TestKnownStreamingUsageAfterTruncatedContent(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			wallet := newWallet(t)
			m := newMeter(t, wallet)
			defer m.Close()
			chunks := []string{
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}]}\n\n",
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n",
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":32}}\n\n",
				"data: [DONE]\n\n",
			}
			input := int64(10)
			if protocol == "anthropic" {
				input = 15
				chunks = []string{
					"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":2,\"cache_creation_input_tokens\":3,\"output_tokens\":0}}}\n\n",
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":32}}\n\n",
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
				}
			}
			client := &http.Client{Transport: reviewTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(&usageChunks{chunks})}, nil
			})}
			g, e := gateway.New(gateway.Config{Models: map[string]gateway.Model{"model": {Protocol: protocol, Endpoint: "https://provider.invalid/generate", Model: "fixture"}}, HTTPClient: client, Billing: m, BillingMaxOutputTokens: 32})
			if e != nil {
				t.Fatal(e)
			}
			_, e = g.Complete(gateway.WithNamespace(context.Background(), "user-one"), ai.Request{RequestID: protocol, Model: "model", MaxOutputTokens: 32, Messages: []ai.Message{{Role: "user", Content: []ai.Block{{Type: "text", Text: "hello"}}}}}, func(ai.Event) error { return nil })
			if e == nil {
				t.Fatal("truncated content must fail")
			}
			if e = m.Flush(context.Background()); e != nil {
				t.Fatal(e)
			}
			wallet.mu.Lock()
			defer wallet.mu.Unlock()
			r := wallet.settled[protocol]
			if !r.Usage.Known || r.Usage.InputTokens != input || r.Usage.OutputTokens != 32 {
				t.Fatalf("known streaming usage lost: %+v", r)
			}
			if protocol == "anthropic" && (r.Usage.CacheReadTokens != 2 || r.Usage.CacheWriteTokens != 3) {
				t.Fatalf("cache counters lost: %+v", r)
			}
		})
	}
}
