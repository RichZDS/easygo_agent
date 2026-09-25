package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"easygo-agent/pkg/ai"
)

func TestToolArgumentsWithoutDeltas(t *testing.T) {
	for _, protocol := range []string{"anthropic", "responses"} {
		for _, args := range []string{`{}`, `{"query":"catalog"}`} {
			for _, mode := range []string{"initial", "done", "item_done", "completed", "chunked", "empty_delta"} {
				if protocol == "anthropic" && (mode == "done" || mode == "item_done" || mode == "completed") {
					continue
				}
				t.Run(protocol+"/"+args+"/"+mode, func(t *testing.T) {
					var frames []string
					if protocol == "anthropic" {
						initial := args
						if mode == "chunked" {
							initial = `{}`
						}
						frames = []string{`{"type":"message_start","message":{"id":"a","usage":{"input_tokens":8,"output_tokens":0}}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-a","name":"workshop_catalog","input":` + initial + `}}`}
						if mode == "chunked" {
							for _, part := range []string{args[:1], args[1:]} {
								frames = append(frames, string(rawJSON(object{"type": "content_block_delta", "index": 0, "delta": object{"type": "input_json_delta", "partial_json": part}})))
							}
						}
						if mode == "empty_delta" {
							frames = append(frames, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
						}
						frames = append(frames, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`, `{"type":"message_stop"}`)
					} else {
						item := object{"type": "function_call", "call_id": "call-a", "name": "workshop_catalog", "arguments": args}
						initial := args
						if mode != "initial" && mode != "empty_delta" {
							initial = ""
						}
						added := object{"type": "function_call", "call_id": "call-a", "name": "workshop_catalog", "arguments": initial}
						frames = []string{string(rawJSON(object{"type": "response.output_item.added", "output_index": 0, "item": added}))}
						if mode == "chunked" {
							for _, part := range []string{args[:1], args[1:]} {
								frames = append(frames, string(rawJSON(object{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": part})))
							}
						}
						if mode == "empty_delta" {
							frames = append(frames, `{"type":"response.function_call_arguments.delta","output_index":0,"delta":""}`)
						}
						if mode != "completed" {
							if mode != "item_done" {
								doneArgs := args
								if mode == "initial" {
									doneArgs = ""
								}
								frames = append(frames, string(rawJSON(object{"type": "response.function_call_arguments.done", "output_index": 0, "arguments": doneArgs})))
							}
							frames = append(frames, string(rawJSON(object{"type": "response.output_item.done", "output_index": 0, "item": item})))
						}
						frames = append(frames, string(rawJSON(object{"type": "response.completed", "response": object{"id": "a", "status": "completed", "output": []any{item}}})))
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sendEvents(w, frames) }))
					defer server.Close()
					g := newTestGateway(t, protocol, server.URL, nil)
					req := ai.Request{Model: "test", MaxOutputTokens: 100, Messages: []ai.Message{{Role: "user", Content: []ai.Block{{Type: "text", Text: "list workflows"}}}}, Tools: []ai.Tool{{Name: "workshop_catalog", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}}
					var streamed, callID, name string
					nonempty := 0
					out, e := g.Complete(context.Background(), req, func(ev ai.Event) error {
						if ev.Type == "tool_call_delta" {
							if ev.Index != 0 {
								t.Errorf("index=%d", ev.Index)
							}
							streamed += ev.Delta
							callID += ev.ID
							name += ev.Name
							if ev.Delta != "" {
								nonempty++
							}
						}
						return nil
					})
					if e != nil {
						t.Fatal(e)
					}
					if len(out.Message.Content) != 1 {
						t.Fatalf("content=%+v", out.Message.Content)
					}
					final := out.Message.Content[0]
					if streamed != string(final.Arguments) || streamed != args || callID != final.ID || name != final.Name {
						t.Fatalf("stream differs from authoritative completion: arguments=%q final=%q id=%q name=%q", streamed, final.Arguments, callID, name)
					}
					want := 1
					if mode == "chunked" {
						want = 2
					}
					if nonempty != want {
						t.Fatalf("argument emissions=%d want=%d", nonempty, want)
					}
				})
			}
		}
	}
}
