package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/services/ai-gateway/ai"
)

func canonicalRequest() ai.Request {
	return ai.Request{Model: "test", RequestID: "req-1", MaxOutputTokens: 32, Messages: []ai.Message{
		{Role: "system", Content: []ai.Block{{Type: "text", Text: "system"}}},
		{Role: "user", Content: []ai.Block{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Content: []ai.Block{{Type: "tool_call", ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"q":"a"}`)}}},
		{Role: "tool", Content: []ai.Block{{Type: "tool_result", ID: "call-1", Text: "tool answer"}}},
	}, Tools: []ai.Tool{{Name: "lookup", Description: "Find it", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}}}
}
func customMapping() *CustomMapping {
	return &CustomMapping{Request: map[string]string{"model": "settings.model", "messages": "payload.messages", "tools": "payload.tools", "max_output_tokens": "settings.limit", "temperature": "settings.temperature", "tool_choice": "settings.choice"}, Response: map[string]string{"id": "result.id", "content": "result.content", "finish_reason": "result.finish", "usage.input_tokens": "stats.input", "usage.output_tokens": "stats.output", "usage.cache_read_tokens": "stats.cached"}}
}
func responseFixture(protocol string) string {
	switch protocol {
	case "chat_completions":
		return `{"id":"out-1","choices":[{"message":{"role":"assistant","content":"hello","tool_calls":[{"type":"function","id":"call-2","function":{"name":"lookup","arguments":"{\"q\":\"b\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":5}}}`
	case "responses":
		return `{"id":"out-1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]},{"type":"function_call","call_id":"call-2","name":"lookup","arguments":"{\"q\":\"b\"}"}],"usage":{"input_tokens":20,"output_tokens":4,"input_tokens_details":{"cached_tokens":5}}}`
	case "anthropic":
		return `{"id":"out-1","content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"call-2","name":"lookup","input":{"q":"b"}}],"stop_reason":"tool_use","usage":{"input_tokens":15,"output_tokens":4,"cache_read_input_tokens":5}}`
	case "custom":
		return `{"result":{"id":"out-1","content":[{"type":"text","text":"hello"},{"type":"tool_call","id":"call-2","name":"lookup","arguments":{"q":"b"}}],"finish":"tool_calls"},"stats":{"input":20,"output":4,"cached":5}}`
	}
	panic(protocol)
}
func newTestGateway(t *testing.T, protocol, url string, observer Observer) *Gateway {
	t.Helper()
	m := Model{Protocol: protocol, Endpoint: url, APIKey: "test-secret", Model: "upstream", Headers: map[string]string{"X-Deployment": "local"}, Price: &Pricing{Currency: "USD", InputPerMillion: 2, OutputPerMillion: 10, CacheReadPerMillion: 1, CacheWritePerMillion: 3}}
	if protocol == "custom" {
		m.Custom = customMapping()
	}
	g, e := New(Config{Models: map[string]Model{"test": m}, Observer: observer})
	if e != nil {
		t.Fatal(e)
	}
	return g
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func TestWireProtocols(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses", "anthropic", "custom"} {
		t.Run(protocol, func(t *testing.T) {
			var seen object
			var observations []Observation
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/full/endpoint" || r.Method != "POST" {
					t.Errorf("wire URL/method = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("X-Deployment") != "local" {
					t.Error("missing configured header")
				}
				if protocol == "anthropic" {
					if r.Header.Get("X-Api-Key") != "test-secret" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
						t.Error("missing anthropic headers")
					}
				} else if r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("missing bearer auth")
				}
				data, _ := io.ReadAll(r.Body)
				var err error
				seen, err = decodeObject(data)
				if err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, responseFixture(protocol))
			}))
			defer s.Close()
			g := newTestGateway(t, protocol, s.URL+"/full/endpoint", func(o Observation) { observations = append(observations, o) })
			out, e := g.Complete(context.Background(), canonicalRequest(), nil)
			if e != nil {
				t.Fatal(e)
			}
			if out.Model != "test" || out.ID != "out-1" || out.FinishReason != "tool_calls" || len(out.Message.Content) != 2 {
				t.Fatalf("bad response: %+v", out)
			}
			if out.Message.Content[0].Text != "hello" || out.Message.Content[1].ID != "call-2" || out.Message.Content[1].Name != "lookup" || string(out.Message.Content[1].Arguments) != `{"q":"b"}` {
				t.Fatalf("blocks = %+v", out.Message.Content)
			}
			if out.Usage != (ai.Usage{Known: true, InputTokens: 20, OutputTokens: 4, CacheReadTokens: 5}) || !out.Cost.Known || out.Cost.Amount != 0.000075 {
				t.Fatalf("usage/cost = %+v %+v", out.Usage, out.Cost)
			}
			if len(observations) != 1 || observations[0].HTTPStatus != 200 || observations[0].RequestID != "req-1" || observations[0].ErrorCode != "" || observations[0].HasFirstDelta {
				t.Fatalf("observations = %+v", observations)
			}
			switch protocol {
			case "chat_completions":
				msgs := arr(seen["messages"])
				if str(seen["model"]) != "upstream" || len(msgs) != 4 || str(obj(msgs[3])["role"]) != "tool" || str(obj(msgs[3])["content"]) != "tool answer" || str(obj(msgs[3])["tool_call_id"]) != "call-1" {
					t.Fatalf("chat request = %+v", seen)
				}
				calls := arr(obj(msgs[2])["tool_calls"])
				if str(obj(obj(calls[0])["function"])["arguments"]) != `{"q":"a"}` {
					t.Fatal("lost tool arguments")
				}
				if _, ok := seen["max_completion_tokens"]; !ok {
					t.Fatal("missing max completion tokens")
				}
			case "responses":
				msgs := arr(seen["input"])
				if len(msgs) != 4 || str(obj(msgs[2])["type"]) != "function_call" || str(obj(msgs[2])["call_id"]) != "call-1" || str(obj(msgs[3])["type"]) != "function_call_output" || str(obj(msgs[3])["output"]) != "tool answer" {
					t.Fatalf("responses request = %+v", seen)
				}
			case "anthropic":
				msgs := arr(seen["messages"])
				if len(msgs) != 3 || len(arr(seen["system"])) != 1 {
					t.Fatalf("anthropic request = %+v", seen)
				}
				result := obj(arr(obj(msgs[2])["content"])[0])
				if str(result["type"]) != "tool_result" || str(result["tool_use_id"]) != "call-1" || str(result["content"]) != "tool answer" {
					t.Fatalf("tool result = %+v", result)
				}
			case "custom":
				if str(obj(seen["settings"])["model"]) != "upstream" || len(arr(obj(seen["payload"])["messages"])) != 4 || len(arr(obj(seen["payload"])["tools"])) != 1 {
					t.Fatalf("custom request = %+v", seen)
				}
			}
		})
	}
}

func streamFixture(protocol string) []string {
	switch protocol {
	case "chat_completions":
		return []string{
			`{"id":"out-1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-2","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"b\"}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":5}}}`,
			`[DONE]`,
		}
	case "responses":
		return []string{
			`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call-2","name":"lookup","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"q\":"}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"b\"}"}`,
			`{"type":"response.completed","response":` + responseFixture("responses") + `}`,
		}
	case "anthropic":
		return []string{
			`{"type":"message_start","message":{"id":"out-1","content":[],"usage":{"input_tokens":15,"output_tokens":0,"cache_read_input_tokens":5}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-2","name":"lookup","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"b\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		}
	}
	panic(protocol)
}
func sendEvents(w http.ResponseWriter, events []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, raw := range events {
		fmt.Fprintf(w, ": keepalive\ndata: %s\n\n", raw)
		w.(http.Flusher).Flush()
	}
}
func TestNativeStreamingIncremental(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				events := streamFixture(protocol)
				split := 1
				if protocol == "anthropic" {
					split = 3
				}
				sendEvents(w, events[:split])
				select {
				case <-release:
				case <-r.Context().Done():
					return
				case <-time.After(3 * time.Second):
					t.Error("client buffered instead of emitting first delta")
					return
				}
				sendEvents(w, events[split:])
			}))
			defer s.Close()
			var obs []Observation
			g := newTestGateway(t, protocol, s.URL, func(o Observation) { obs = append(obs, o) })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var events []ai.Event
			out, e := g.Complete(ctx, canonicalRequest(), func(ev ai.Event) error { events = append(events, ev); once.Do(func() { close(release) }); return nil })
			once.Do(func() { close(release) })
			if e != nil {
				t.Fatal(e)
			}
			if out.Message.Content[0].Text != "hello" || len(out.Message.Content) != 2 || out.Message.Content[1].ID != "call-2" || string(out.Message.Content[1].Arguments) != `{"q":"b"}` {
				t.Fatalf("response = %+v", out)
			}
			if out.Usage.OutputTokens != 4 || out.Usage.InputTokens != 20 || out.Usage.CacheReadTokens != 5 {
				t.Fatalf("usage = %+v", out.Usage)
			}
			var args string
			for _, ev := range events {
				if ev.Type == "tool_call_delta" {
					if ev.Index != 1 {
						t.Fatalf("tool event index = %d", ev.Index)
					}
					args += ev.Delta
				}
			}
			if args != `{"q":"b"}` {
				t.Fatalf("arguments = %s", args)
			}
			if len(obs) != 1 || !obs[0].HasFirstDelta || obs[0].ErrorCode != "" || calls.Load() != 1 {
				t.Fatalf("observations = %+v calls=%d", obs, calls.Load())
			}
		})
	}
}

func TestStreamFailures(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses", "anthropic"} {
		for _, kind := range []string{"truncated", "provider_error", "incomplete", "malformed", "callback", "cancel"} {
			t.Run(protocol+"/"+kind, func(t *testing.T) {
				events := streamFixture(protocol)
				split := 1
				if protocol == "anthropic" {
					split = 3
				}
				events = events[:split]
				switch kind {
				case "provider_error":
					events = append(events, `{"type":"error","error":{"message":"SECRET PROMPT ECHO"}}`)
				case "incomplete":
					switch protocol {
					case "chat_completions":
						events = append(events, `{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`)
					case "responses":
						events = append(events, `{"type":"response.incomplete"}`)
					case "anthropic":
						events = append(events, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`)
					}
				case "malformed":
					events = append(events, `{"type":`)
				}
				var calls atomic.Int32
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					sendEvents(w, events)
					if kind == "cancel" || kind == "callback" {
						<-r.Context().Done()
					}
				}))
				defer s.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				sentinel := errors.New("PRIVATE CALLBACK MESSAGE")
				var obs []Observation
				g := newTestGateway(t, protocol, s.URL, func(o Observation) { obs = append(obs, o) })
				seen := 0
				_, err := g.Complete(ctx, canonicalRequest(), func(ai.Event) error {
					seen++
					if kind == "callback" {
						return sentinel
					}
					if kind == "cancel" {
						cancel()
					}
					return nil
				})
				want := map[string]string{"truncated": "truncated_stream", "provider_error": "upstream_error", "incomplete": "incomplete_response", "malformed": "invalid_response", "callback": "callback_error", "cancel": "canceled"}[kind]
				requireCode(t, err, want)
				if seen == 0 || calls.Load() != 1 || len(obs) != 1 || obs[0].ErrorCode != want {
					t.Fatalf("seen=%d calls=%d observations=%+v", seen, calls.Load(), obs)
				}
				if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "PRIVATE") {
					t.Fatalf("unsafe error: %s", err)
				}
				if kind == "callback" && !errors.Is(err, sentinel) {
					t.Fatal("callback error cause lost")
				}
				if kind == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal("context cause lost")
				}
			})
		}
	}
}

func TestParametersAndConfigurationProtection(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body object
		json.NewDecoder(r.Body).Decode(&body)
		if body["vendor_temperature"] != 0.7 || body["top_p"] != 0.9 {
			t.Errorf("parameters = %+v", body)
		}
		fmt.Fprint(w, responseFixture("chat_completions"))
	}))
	defer s.Close()
	m := Model{Protocol: "chat_completions", Endpoint: s.URL, Model: "upstream", Parameters: map[string]json.RawMessage{"temperature": json.RawMessage(`0.1`), "top_p": json.RawMessage(`0.9`)}, ParameterMap: map[string]string{"temperature": "vendor_temperature"}}
	g, e := New(Config{Models: map[string]Model{"test": m}})
	if e != nil {
		t.Fatal(e)
	}
	m.Parameters["top_p"] = json.RawMessage(`0`)
	req := canonicalRequest()
	temp := 0.7
	req.Temperature = &temp
	if _, e = g.Complete(context.Background(), req, nil); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"model", "messages", "stream", "input", "system", "tools", "Authorization", "api_key", "headers", "n", "background", "previous_response_id"} {
		t.Run(field, func(t *testing.T) {
			r := canonicalRequest()
			r.Parameters = map[string]json.RawMessage{field: json.RawMessage(`"override"`)}
			_, e := g.Complete(context.Background(), r, nil)
			requireCode(t, e, "invalid_parameters")
		})
	}
	for _, field := range []string{"model", "messages", "stream", "Authorization"} {
		m.ParameterMap = map[string]string{"temperature": field}
		_, e := New(Config{Models: map[string]Model{"test": m}})
		requireCode(t, e, "invalid_config")
	}
	m.ParameterMap = nil
	for _, field := range []string{"Authorization", "X-Api-Key", "Content-Type", "Host", "X-Test\nBad"} {
		m.Headers = map[string]string{field: "value"}
		_, e := New(Config{Models: map[string]Model{"test": m}})
		requireCode(t, e, "invalid_config")
	}
}

func TestCachePricingAndUnknownUsage(t *testing.T) {
	p := &Pricing{Currency: "USD", InputPerMillion: 2, OutputPerMillion: 10, CacheReadPerMillion: 1, CacheWritePerMillion: 3}
	u := ai.Usage{Known: true, InputTokens: 100, OutputTokens: 10, CacheReadTokens: 30, CacheWriteTokens: 20}
	if got := calculateCost(u, p); !got.Known || got.Amount != 0.00029 {
		t.Fatalf("cost = %+v", got)
	}
	for _, u := range []ai.Usage{{}, {Known: true, InputTokens: 10, CacheReadTokens: 11}, {Known: true, InputTokens: -1}} {
		if calculateCost(u, p).Known {
			t.Fatal("unknown/invalid usage was priced")
		}
	}
	if calculateCost(ai.Usage{Known: true}, nil).Known {
		t.Fatal("missing price is not free")
	}
	o, _ := decodeObject([]byte(`{"input_tokens":50,"output_tokens":10,"cache_read_input_tokens":30,"cache_creation_input_tokens":20}`))
	got, e := parseUsage(o, "anthropic")
	if e != nil || got != u {
		t.Fatalf("normalized usage = %+v %v", got, e)
	}
	missing, _ := decodeObject([]byte(`{"input_tokens":50}`))
	got, e = parseUsage(missing, "responses")
	if e != nil || got.Known {
		t.Fatalf("partial usage = %+v %v", got, e)
	}
}

func TestObservationOnFailures(t *testing.T) {
	var calls atomic.Int32
	var obs []Observation
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"PROMPT test-secret"}}`)
	}))
	defer s.Close()
	g := newTestGateway(t, "responses", s.URL, func(o Observation) { obs = append(obs, o) })
	_, err := g.Complete(context.Background(), canonicalRequest(), nil)
	requireCode(t, err, "upstream_http_error")
	if strings.Contains(err.Error(), "test-secret") {
		t.Fatal("error leaks key")
	}
	req := canonicalRequest()
	req.Model = "missing"
	_, err = g.Complete(context.Background(), req, nil)
	requireCode(t, err, "unknown_model")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = g.Complete(ctx, canonicalRequest(), nil)
	requireCode(t, err, "canceled")
	if len(obs) != 3 || obs[0].HTTPStatus != 429 || obs[1].HTTPStatus != 0 || obs[2].HTTPStatus != 0 || calls.Load() != 1 {
		t.Fatalf("observations=%+v calls=%d", obs, calls.Load())
	}
	for _, o := range obs {
		b, _ := json.Marshal(o)
		if strings.Contains(string(b), "test-secret") || strings.Contains(string(b), "PROMPT") {
			t.Fatal("observation leaks payload")
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer s.Close()
	g := newTestGateway(t, "responses", s.URL, nil)
	_, err := g.Complete(context.Background(), canonicalRequest(), nil)
	requireCode(t, err, "upstream_http_error")
	if followed.Load() != 0 {
		t.Fatal("credential-bearing redirect followed")
	}
}

func TestCustomMappingFailures(t *testing.T) {
	for _, kind := range []string{"missing", "bad_path", "overlap", "bad_response", "stream", "unsupported_tools", "override"} {
		t.Run(kind, func(t *testing.T) {
			mapping := customMapping()
			m := Model{Protocol: "custom", Endpoint: "http://localhost", Model: "u", Custom: mapping}
			switch kind {
			case "missing":
				delete(mapping.Request, "messages")
			case "bad_path":
				mapping.Response["id"] = "result[0].id"
			case "overlap":
				mapping.Request["model"] = "payload"
			}
			if kind == "missing" || kind == "bad_path" || kind == "overlap" {
				_, e := New(Config{Models: map[string]Model{"test": m}})
				requireCode(t, e, "invalid_config")
				return
			}
			if kind == "bad_response" {
				_, e := decodeResponse(m, "test", []byte(`{"result":{"id":"x"}}`))
				requireCode(t, e, "invalid_response")
				return
			}
			req := canonicalRequest()
			if kind == "unsupported_tools" {
				delete(mapping.Request, "tools")
			}
			if kind == "override" {
				req.Parameters = map[string]json.RawMessage{"payload": json.RawMessage(`{}`)}
			}
			_, e := encodeRequest(m, req, kind == "stream")
			code := "unsupported_capability"
			if kind == "override" {
				code = "invalid_parameters"
			}
			requireCode(t, e, code)
		})
	}
}

type billingFunc func(context.Context, Reservation) (func(Observation) error, error)

func (f billingFunc) Reserve(ctx context.Context, r Reservation) (func(Observation) error, error) {
	return f(ctx, r)
}

func TestBilledCustomProtocolRejectedByNew(t *testing.T) {
	custom := Model{Protocol: "custom", Endpoint: "http://localhost", Model: "u", Custom: customMapping()}
	standard := Model{Protocol: "responses", Endpoint: "http://localhost", Model: "u"}
	billing := billingFunc(func(context.Context, Reservation) (func(Observation) error, error) {
		t.Error("New reserved credits")
		return nil, errors.New("unused")
	})
	if _, e := New(Config{Models: map[string]Model{"custom": custom}}); e != nil {
		t.Fatalf("unbilled custom rejected: %v", e)
	}
	if _, e := New(Config{Models: map[string]Model{"standard": standard}, Billing: billing}); e != nil {
		t.Fatalf("billed standard protocol rejected: %v", e)
	}
	for name, models := range map[string]map[string]Model{"custom only": {"custom": custom}, "mixed": {"standard": standard, "custom": custom}} {
		t.Run(name, func(t *testing.T) {
			_, e := New(Config{Models: models, Billing: billing})
			requireCode(t, e, "invalid_config")
		})
	}
}

func TestConcurrentComplete(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, responseFixture("responses")) }))
	defer s.Close()
	var count atomic.Int32
	g := newTestGateway(t, "responses", s.URL, func(Observation) { count.Add(1) })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := g.Complete(context.Background(), canonicalRequest(), nil); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 8 {
		t.Fatal("missing observations")
	}
}

func TestResponsesFinalIsAuthoritative(t *testing.T) {
	events := streamFixture("responses")
	events[0] = strings.ReplaceAll(events[0], "hello", "provisional")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sendEvents(w, events) }))
	defer s.Close()
	g := newTestGateway(t, "responses", s.URL, nil)
	var first string
	out, e := g.Complete(context.Background(), canonicalRequest(), func(ev ai.Event) error {
		if ev.Type == "text_delta" {
			first += ev.Delta
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if first != "provisional" || out.Message.Content[0].Text != "hello" {
		t.Fatalf("first=%s out=%+v", first, out)
	}
}

func TestBoundedResponses(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				fmt.Fprint(w, strings.Repeat("x", 2048))
			}))
			defer s.Close()
			g, e := New(Config{Models: map[string]Model{"test": {Protocol: "responses", Endpoint: s.URL, Model: "u"}}, MaxResponseBytes: 1024, MaxStreamBytes: 1024, MaxEventBytes: 128})
			if e != nil {
				t.Fatal(e)
			}
			req := ai.Request{Model: "test", Messages: []ai.Message{{Role: "user", Content: []ai.Block{{Type: "text", Text: "hi"}}}}}
			var emit func(ai.Event) error
			if stream {
				emit = func(ai.Event) error { return nil }
			}
			_, err := g.Complete(context.Background(), req, emit)
			if err == nil {
				t.Fatal("unbounded response accepted")
			}
		})
	}
}

func TestImagesAndErrorToolResults(t *testing.T) {
	r := canonicalRequest()
	r.Messages[1].Content = append(r.Messages[1].Content, ai.Block{Type: "image", MediaType: "image/png", Data: "AAAA"})
	r.Messages[3].Content[0].IsError = true
	for _, p := range []string{"chat_completions", "responses", "anthropic"} {
		m := Model{Protocol: p, Model: "u"}
		o, e := encodeRequest(m, r, false)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(o)
		if !strings.Contains(string(raw), "image") || !strings.Contains(string(raw), "is_error") {
			t.Fatalf("image or error flag missing: %s", raw)
		}
	}
}

func TestSSEMultilineAndMissingSeparator(t *testing.T) {
	var got []string
	e := readSSE(strings.NewReader("event: test\r\ndata: {\r\ndata: \"a\":1}\r\n\r\n"), 1024, 128, func(event string, raw []byte) error { got = append(got, event, string(raw)); return errStreamDone })
	if e != nil || !reflect.DeepEqual(got, []string{"test", "{\n\"a\":1}"}) {
		t.Fatalf("got=%v err=%v", got, e)
	}
	e = readSSE(strings.NewReader("data: [DONE]\n"), 1024, 128, func(string, []byte) error { return errStreamDone })
	requireCode(t, e, "truncated_stream")
}

func TestParameterDefaultsOverrideByCanonicalRequest(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			key := map[string]string{"chat_completions": "max_completion_tokens", "responses": "max_output_tokens", "anthropic": "max_tokens"}[protocol]
			m := Model{Protocol: protocol, Model: "u", Parameters: map[string]json.RawMessage{key: json.RawMessage(`100`)}}
			out, e := encodeRequest(m, canonicalRequest(), false)
			if e != nil {
				t.Fatal(e)
			}
			if out[key] != float64(32) {
				t.Fatalf("typed request failed to override native default: %+v", out)
			}
		})
	}
}
func TestMalformedProviderResponses(t *testing.T) {
	for _, tc := range []struct{ protocol, raw string }{
		{"chat_completions", `{"choices":[{"message":{"content":null,"tool_calls":false},"finish_reason":"tool_calls"}]}`},
		{"chat_completions", `{"choices":[{"message":{"content":""},"finish_reason":"tool_calls"}]}`},
		{"responses", `{"status":"completed","output":[{"type":"message","content":null}]}`},
		{"responses", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":false}]}]}`},
		{"anthropic", `{"content":[{"type":"thinking","thinking":"no signature"}],"stop_reason":"end_turn"}`},
		{"anthropic", `{"content":[{"type":"text","text":7}],"stop_reason":"end_turn"}`},
	} {
		t.Run(tc.protocol+tc.raw, func(t *testing.T) {
			_, e := decodeResponse(Model{Protocol: tc.protocol}, "test", []byte(tc.raw))
			if e == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
	for _, protocol := range []string{"chat_completions", "responses", "anthropic"} {
		o, _ := decodeObject([]byte(`{"input_tokens":-1,"output_tokens":2,"prompt_tokens":-1,"completion_tokens":2}`))
		_, e := parseUsage(o, protocol)
		requireCode(t, e, "invalid_response")
	}
}

func TestCompleteResultAndStreamFlag(t *testing.T) {
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
			seen := 0
			var emit func(ai.Event) error
			if stream {
				emit = func(ev ai.Event) error { seen++; return nil }
			}
			out, e := g.Complete(context.Background(), canonicalRequest(), emit)
			if e != nil {
				t.Fatal(e)
			}
			if out.Model != "test" || len(out.Message.Content) != 2 || out.Usage.OutputTokens != 4 || !out.Cost.Known || observed.Load() != 1 {
				t.Fatalf("out=%+v observed=%d", out, observed.Load())
			}
			if stream && seen == 0 {
				t.Fatal("no delta")
			}
		})
	}
}
func TestCompleteDoesNotReturnPartialFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"incomplete","output":[{"type":"message","content":[{"type":"output_text","text":"private partial"}]}]}`)
	}))
	defer s.Close()
	g := newTestGateway(t, "responses", s.URL, nil)
	out, e := g.Complete(context.Background(), canonicalRequest(), nil)
	if e == nil {
		t.Fatal("incomplete response accepted")
	}
	if raw, _ := json.Marshal(out); strings.Contains(string(raw), "private partial") || strings.Contains(e.Error(), "private partial") {
		t.Fatalf("partial output exposed: %s %v", raw, e)
	}
}
