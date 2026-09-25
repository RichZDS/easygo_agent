package chatmodel

import (
	"context"
	"easygo-agent/pkg/ai"
	"easygo-agent/pkg/gateway"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicNoArgumentToolStreamMatchesCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`{"type":"message_start","message":{"id":"a","usage":{"input_tokens":8,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-a","name":"workshop_catalog","input":{}}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		}
		for _, frame := range frames {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}
	}))
	defer server.Close()
	g, err := gateway.New(gateway.Config{Models: map[string]gateway.Model{"m": {Protocol: "anthropic", Endpoint: server.URL, Model: "upstream", Parameters: map[string]json.RawMessage{"max_tokens": json.RawMessage(`100`)}}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := g.Complete(context.Background(), ai.Request{Model: "m", Messages: []ai.Message{{Role: "user", Content: []ai.Block{{Type: "text", Text: "list workflows"}}}}}, func(ai.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("gateway final: arguments=%s finish=%s", raw.Message.Content[0].Arguments, raw.FinishReason)
	adapter, _ := NewAdapter(g, "m")
	reader, err := adapter.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("list workflows")}, model.WithTools([]*schema.ToolInfo{{Name: "workshop_catalog"}}))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for {
		_, err = reader.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("valid no-argument tool stream failed in adapter: %v", err)
		}
	}
}
