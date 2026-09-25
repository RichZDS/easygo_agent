package chatmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"easygo-agent/internal/config"
	"easygo-agent/pkg/ai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestGatewayAdapterHTTPProtocols(t *testing.T) {
	fixtures := map[string]string{
		"chat_completions": `{"id":"c1","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		"responses":        `{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`,
		"anthropic":        `{"id":"a1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`,
	}
	for protocol, body := range fixtures {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/exact/endpoint" {
					t.Errorf("full endpoint changed: %s", r.URL.Path)
				}
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req["model"] != "upstream" || req["temperature"] != 0.5 {
					t.Errorf("request mapping: %+v", req)
				}
				if protocol == "anthropic" {
					if r.Header.Get("x-api-key") != "fixture-key" {
						t.Error("missing anthropic key")
					}
				} else if r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Error("missing bearer")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			adapter, err := New(context.Background(), config.ModelConfig{Name: "upstream", Protocol: protocol, Endpoint: server.URL + "/exact/endpoint", APIKey: "fixture-key", Timeout: time.Second, Pricing: &config.ModelPricing{Currency: "USD", InputPerMillion: 1, OutputPerMillion: 2}})
			if err != nil {
				t.Fatal(err)
			}
			out, err := adapter.Generate(context.Background(), []*schema.AgenticMessage{schema.SystemAgenticMessage("instruction"), schema.UserAgenticMessage("input")}, model.WithTemperature(0.5), model.WithMaxTokens(100))
			if err != nil {
				t.Fatal(err)
			}
			if out.ContentBlocks[0].AssistantGenText.Text != "hello" || out.ResponseMeta.TokenUsage.TotalTokens != 12 {
				t.Fatalf("output mapping: %+v", out)
			}
			encoded, _ := json.Marshal(out.ResponseMeta.Extension)
			if !strings.Contains(string(encoded), `"known":true`) {
				t.Fatalf("cost metadata missing: %s", encoded)
			}
		})
	}
}

func TestGatewayAdapterStreamingHTTPAndTruncation(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint(truncated), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("base_url shorthand changed: %s", r.URL.Path)
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["stream"] != true {
					t.Error("stream flag missing")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"s1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
				w.(http.Flusher).Flush()
				if !truncated {
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
				}
			}))
			defer server.Close()
			a, err := New(context.Background(), config.ModelConfig{Name: "model", BaseURL: server.URL + "/v1", APIKey: "fixture", Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			reader, err := a.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("hi")})
			if err != nil {
				t.Fatal(err)
			}
			m, err := drain(reader)
			if truncated {
				if err == nil {
					t.Fatal("truncated HTTP stream became success")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(m.ContentBlocks) != 1 || m.ContentBlocks[0].AssistantGenText.Text != "hello" || m.ResponseMeta.TokenUsage.TotalTokens != 12 {
				t.Fatalf("stream output: %+v", m)
			}
		})
	}
}

func TestGatewaySignedReasoningStreamStorageContinuation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `event: message_start
data: {"type":"message_start","message":{"id":"signed-1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reason"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"fixture-signature"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool-1","name":"echo","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"value\":1}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`)
			return
		}
		messages := req["messages"].([]any)
		assistant := messages[len(messages)-2].(map[string]any)
		blocks := assistant["content"].([]any)
		reasoning := blocks[0].(map[string]any)
		if reasoning["type"] != "thinking" || reasoning["thinking"] != "reason" || reasoning["signature"] != "fixture-signature" {
			t.Errorf("signed continuation lost: %+v", reasoning)
		}
		result := messages[len(messages)-1].(map[string]any)["content"].([]any)[0].(map[string]any)
		if result["tool_use_id"] != "tool-1" {
			t.Errorf("tool correlation lost: %+v", result)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"signed-2","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":2}}`)
	}))
	defer server.Close()
	a, err := New(context.Background(), config.ModelConfig{Name: "fixture", Protocol: "anthropic", Endpoint: server.URL + "/messages", APIKey: "fixture", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	options := []model.Option{model.WithMaxTokens(100), model.WithTools([]*schema.ToolInfo{{Name: "echo"}})}
	reader, err := a.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("go")}, options...)
	if err != nil {
		t.Fatal(err)
	}
	response, err := drain(reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var stored schema.AgenticMessage
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	result := MessageFromAI(ai.Message{Role: "tool", Content: []ai.Block{{Type: "tool_result", Name: "echo", ID: "tool-1", Text: "ok"}}})
	final, err := a.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("go"), &stored, result}, options...)
	if err != nil {
		t.Fatal(err)
	}
	if final.ContentBlocks[0].AssistantGenText.Text != "done" {
		t.Fatal("missing final answer")
	}
}
