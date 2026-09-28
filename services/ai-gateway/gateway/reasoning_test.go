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

	"easygo-agent/services/ai-gateway/ai"
)

func reasoningFixture(protocol string) string {
	o, _ := decodeObject([]byte(responseFixture(protocol)))
	switch protocol {
	case "chat_completions":
		obj(obj(arr(o["choices"])[0])["message"])["reasoning_content"] = "think twice"
	case "responses":
		o["output"] = append([]any{object{"type": "reasoning", "id": "rs-1", "summary": []any{object{"type": "summary_text", "text": "think twice"}}, "encrypted_content": "opaque-encrypted"}}, arr(o["output"])...)
	case "anthropic":
		o["content"] = append([]any{object{"type": "thinking", "thinking": "think twice", "signature": "opaque-signature"}, object{"type": "redacted_thinking", "data": "opaque-redacted"}}, arr(o["content"])...)
	}
	b, _ := json.Marshal(o)
	return string(b)
}
func reasoningStream(protocol string) []string {
	switch protocol {
	case "chat_completions":
		return append([]string{`{"choices":[{"index":0,"delta":{"reasoning_content":"think "}}]}`, `{"choices":[{"index":0,"delta":{"reasoning_content":"twice"}}]}`}, streamFixture(protocol)...)
	case "responses":
		events := []string{`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs-1","summary":[]}}`, `{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"think twice"}`}
		for _, raw := range streamFixture(protocol) {
			o, _ := decodeObject([]byte(raw))
			if str(o["type"]) == "response.completed" {
				events = append(events, `{"type":"response.completed","response":`+reasoningFixture(protocol)+`}`)
				continue
			}
			if n, ok := number(o["output_index"]); ok {
				o["output_index"] = n + 1
			}
			events = append(events, string(rawJSON(o)))
		}
		return events
	case "anthropic":
		base := streamFixture(protocol)
		events := []string{base[0], `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think twice"}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque-"}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signature"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque-redacted"}}`, `{"type":"content_block_stop","index":1}`}
		for _, raw := range base[1:] {
			o, _ := decodeObject([]byte(raw))
			if n, ok := number(o["index"]); ok {
				o["index"] = n + 2
			}
			events = append(events, string(rawJSON(o)))
		}
		return events
	}
	panic(protocol)
}
func TestReasoningToolRoundTrip(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", protocol, stream), func(t *testing.T) {
				var calls atomic.Int32
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					o, e := decodeObject(raw)
					if e != nil {
						t.Error(e)
					}
					if calls.Add(1) == 1 {
						if stream {
							sendEvents(w, reasoningStream(protocol))
						} else {
							fmt.Fprint(w, reasoningFixture(protocol))
						}
						return
					}
					if !strings.Contains(string(raw), "tool answer 2") {
						t.Error("next model did not receive tool result")
					}
					switch protocol {
					case "chat_completions":
						msgs := arr(o["messages"])
						assistant := obj(msgs[len(msgs)-2])
						if str(assistant["reasoning_content"]) != "think twice" {
							t.Errorf("reasoning lost: %s", raw)
						}
					case "responses":
						found := false
						for _, v := range arr(o["input"]) {
							item := obj(v)
							if str(item["type"]) == "reasoning" {
								found = true
								if str(item["id"]) != "rs-1" || str(item["encrypted_content"]) != "opaque-encrypted" || len(arr(item["summary"])) != 1 {
									t.Errorf("reasoning state lost: %s", raw)
								}
							}
						}
						if !found {
							t.Errorf("no reasoning item: %s", raw)
						}
					case "anthropic":
						msgs := arr(o["messages"])
						blocks := arr(obj(msgs[len(msgs)-2])["content"])
						if len(blocks) != 4 || str(obj(blocks[0])["signature"]) != "opaque-signature" || str(obj(blocks[0])["thinking"]) != "think twice" || str(obj(blocks[1])["data"]) != "opaque-redacted" {
							t.Errorf("thinking state lost: %s", raw)
						}
					}
					switch protocol {
					case "chat_completions":
						fmt.Fprint(w, `{"choices":[{"message":{"content":"final"},"finish_reason":"stop"}]}`)
					case "responses":
						fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"final"}]}]}`)
					case "anthropic":
						fmt.Fprint(w, `{"content":[{"type":"text","text":"final"}],"stop_reason":"end_turn"}`)
					}
				}))
				defer s.Close()
				g := newTestGateway(t, protocol, s.URL, nil)
				req := canonicalRequest()
				var emit func(ai.Event) error
				if stream {
					emit = func(ai.Event) error { return nil }
				}
				first, e := g.Complete(context.Background(), req, emit)
				if e != nil {
					t.Fatal(e)
				}
				states := 0
				for _, b := range first.Message.Content {
					if b.Type == "reasoning" {
						states++
						if len(b.ProviderState) == 0 {
							t.Fatal("reasoning state absent")
						}
					}
				}
				if states == 0 {
					t.Fatal("reasoning omitted")
				}
				// Round-trip through JSON as a remote gateway or persisted transcript would.
				raw, _ := json.Marshal(first.Message)
				var assistant ai.Message
				if e = json.Unmarshal(raw, &assistant); e != nil {
					t.Fatal(e)
				}
				req.Messages = append(req.Messages, assistant, ai.Message{Role: "tool", Content: []ai.Block{{Type: "tool_result", ID: "call-2", Text: "tool answer 2"}}})
				final, e := g.Complete(context.Background(), req, nil)
				if e != nil {
					t.Fatal(e)
				}
				if len(final.Message.Content) != 1 || final.Message.Content[0].Text != "final" || calls.Load() != 2 {
					t.Fatalf("final=%+v calls=%d", final, calls.Load())
				}
			})
		}
	}
}
func TestReasoningStateValidation(t *testing.T) {
	for _, p := range []string{"chat_completions", "responses", "anthropic", "custom"} {
		t.Run(p, func(t *testing.T) {
			r := canonicalRequest()
			r.Messages[2].Content = append([]ai.Block{{Type: "reasoning", Text: "ordinary reasoning"}}, r.Messages[2].Content...)
			m := Model{Protocol: p, Model: "upstream", Custom: customMapping()}
			if _, e := encodeRequest(m, r, false); e != nil {
				t.Fatalf("ordinary reasoning failed: %v", e)
			}
			r.Messages[2].Content[0].ProviderState = state("other_protocol", object{"type": "reasoning"})
			_, e := encodeRequest(m, r, false)
			requireCode(t, e, "unsupported_capability")
			r.Messages[2].Content[0].ProviderState = json.RawMessage(`{`)
			_, e = encodeRequest(m, r, false)
			requireCode(t, e, "invalid_request")
		})
	}
}
func TestEncryptedReasoningWithEmptySummary(t *testing.T) {
	r, e := decodeResponse(Model{Protocol: "responses"}, "test", []byte(`{"status":"completed","output":[{"type":"reasoning","id":"rs","summary":[],"encrypted_content":"opaque"}]}`))
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Message.Content) != 1 || r.Message.Content[0].Type != "reasoning" || len(r.Message.Content[0].ProviderState) == 0 {
		t.Fatalf("reasoning = %+v", r)
	}
}

func TestEmptyChatReasoningPreserved(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var r ai.Response
			var e error
			if stream {
				r, e = streamChat(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), "test", 4096, 1024, func(ai.Event) error { return nil })
			} else {
				r, e = decodeResponse(Model{Protocol: "chat_completions"}, "test", []byte(`{"choices":[{"message":{"content":"ok","reasoning_content":""},"finish_reason":"stop"}]}`))
			}
			if e != nil {
				t.Fatal(e)
			}
			req := canonicalRequest()
			req.Messages = append(req.Messages, r.Message)
			o, e := encodeRequest(Model{Protocol: "chat_completions", Model: "u"}, req, false)
			if e != nil {
				t.Fatal(e)
			}
			msgs := arr(o["messages"])
			last := obj(msgs[len(msgs)-1])
			v, ok := last["reasoning_content"]
			if !ok || v != "" {
				t.Fatalf("empty reasoning lost: %+v", last)
			}
		})
	}
}
