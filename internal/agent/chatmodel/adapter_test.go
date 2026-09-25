package chatmodel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"easygo-agent/pkg/ai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type fakeClient func(context.Context, ai.Request, func(ai.Event) error) (ai.Response, error)

func (f fakeClient) Complete(c context.Context, r ai.Request, e func(ai.Event) error) (ai.Response, error) {
	return f(c, r, e)
}
func drain(reader *schema.StreamReader[*schema.AgenticMessage]) (*schema.AgenticMessage, error) {
	defer reader.Close()
	var chunks []*schema.AgenticMessage
	for {
		m, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, m)
	}
	return schema.ConcatAgenticMessages(chunks)
}
func TestAdapterOptionsAndMessageProjection(t *testing.T) {
	a, _ := NewAdapter(fakeClient(func(_ context.Context, r ai.Request, emit func(ai.Event) error) (ai.Response, error) {
		if r.Model != "other" || r.MaxOutputTokens != 123 || r.Temperature == nil || *r.Temperature != 0.5 || string(r.Parameters["stop"]) != `["stop"]` || len(r.Tools) != 1 || r.Tools[0].Name != "echo" || r.ToolChoice != "required" || emit != nil {
			t.Fatalf("options lost: %+v", r)
		}
		if r.Messages[1].Role != "tool" || !r.Messages[1].Content[0].IsError || r.Messages[1].Content[0].ID != "call" {
			t.Fatalf("tool result lost: %+v", r.Messages)
		}
		return ai.Response{Message: ai.Message{Role: "assistant", Content: []ai.Block{{Type: "text", Text: "ok"}}}, Usage: ai.Usage{Known: true, InputTokens: 10, OutputTokens: 2, CacheReadTokens: 3}, Cost: ai.Cost{Known: true, Currency: "USD", Amount: 0.1}}, nil
	}), "alias")
	m, err := a.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("question"), MessageFromAI(ai.Message{Role: "tool", Content: []ai.Block{{Type: "tool_result", Name: "echo", ID: "call", Text: "failed", IsError: true}}})}, model.WithModel("other"), model.WithMaxTokens(123), model.WithTemperature(0.5), model.WithStop([]string{"stop"}), model.WithTools([]*schema.ToolInfo{{Name: "echo"}, {Name: "other"}}), model.WithToolChoice(schema.ToolChoiceForced, "echo"))
	if err != nil {
		t.Fatal(err)
	}
	if m.ResponseMeta.TokenUsage.TotalTokens != 12 || m.ResponseMeta.TokenUsage.PromptTokenDetails.CachedTokens != 3 {
		t.Fatal("usage lost")
	}
}
func TestStreamSlowConsumerKeepsTerminalMetadataAndOpaqueState(t *testing.T) {
	state := json.RawMessage(`{"protocol":"anthropic","value":{"signature":"opaque"}}`)
	returned := make(chan struct{})
	response := ai.Response{Message: ai.Message{Role: "assistant", Content: []ai.Block{{Type: "reasoning", Text: "thinking", ProviderState: state}, {Type: "tool_call", ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{"a":1}`)}}}, Usage: ai.Usage{Known: true, InputTokens: 11, OutputTokens: 4}, Cost: ai.Cost{Known: true, Currency: "USD", Amount: 0.2}}
	a, _ := NewAdapter(fakeClient(func(_ context.Context, _ ai.Request, emit func(ai.Event) error) (ai.Response, error) {
		defer close(returned)
		for _, e := range []ai.Event{{Type: "reasoning_delta", Index: 0, Delta: "thinking"}, {Type: "tool_call_delta", Index: 1, ID: "call-1", Name: "echo", Delta: `{"a":`}, {Type: "tool_call_delta", Index: 1, Delta: `1}`}} {
			if err := emit(e); err != nil {
				return ai.Response{}, err
			}
		}
		return response, nil
	}), "alias")
	reader, err := a.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	<-returned
	// Let producer cleanup run before reading its buffered terminal metadata.
	time.Sleep(20 * time.Millisecond)
	m, err := drain(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ContentBlocks) != 2 || m.ResponseMeta == nil || m.ResponseMeta.TokenUsage.TotalTokens != 15 {
		t.Fatalf("terminal lost: %+v", m)
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var stored schema.AgenticMessage
	if err = json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	projected, err := MessagesToAI([]*schema.AgenticMessage{&stored})
	if err != nil {
		t.Fatal(err)
	}
	if string(projected[0].Content[0].ProviderState) != string(state) || projected[0].Content[1].ID != "call-1" || string(projected[0].Content[1].Arguments) != `{"a":1}` {
		t.Fatalf("state or tool call lost: %+v", projected)
	}
	next, _ := NewAdapter(fakeClient(func(_ context.Context, r ai.Request, _ func(ai.Event) error) (ai.Response, error) {
		if string(r.Messages[0].Content[0].ProviderState) != string(state) || r.Messages[1].Content[0].ID != "call-1" {
			t.Fatalf("roundtrip request lost state: %+v", r.Messages)
		}
		return ai.Response{Message: ai.Message{Role: "assistant", Content: []ai.Block{{Type: "text", Text: "done"}}}}, nil
	}), "alias")
	if _, err = next.Generate(context.Background(), []*schema.AgenticMessage{&stored, MessageFromAI(ai.Message{Role: "tool", Content: []ai.Block{{Type: "tool_result", Name: "echo", ID: "call-1", Text: "ok"}}})}); err != nil {
		t.Fatal(err)
	}
}
func TestStreamFailureIsNotEOF(t *testing.T) {
	failure := errors.New("upstream truncated")
	a, _ := NewAdapter(fakeClient(func(_ context.Context, _ ai.Request, emit func(ai.Event) error) (ai.Response, error) {
		if err := emit(ai.Event{Type: "text_delta", Delta: "partial"}); err != nil {
			return ai.Response{}, err
		}
		return ai.Response{}, failure
	}), "alias")
	reader, err := a.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = drain(reader); !errors.Is(err, failure) {
		t.Fatalf("failure swallowed: %v", err)
	}
}
func TestStreamRejectsInconsistentFinalText(t *testing.T) {
	a, _ := NewAdapter(fakeClient(func(_ context.Context, _ ai.Request, emit func(ai.Event) error) (ai.Response, error) {
		if err := emit(ai.Event{Type: "text_delta", Delta: "prefix"}); err != nil {
			return ai.Response{}, err
		}
		return ai.Response{Message: ai.Message{Role: "assistant", Content: []ai.Block{{Type: "text", Text: "different"}}}}, nil
	}), "alias")
	reader, _ := a.Stream(context.Background(), nil)
	if _, err := drain(reader); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("mismatch accepted: %v", err)
	}
}
func TestCancelBlockedProviderRead(t *testing.T) {
	done := make(chan struct{})
	a, _ := NewAdapter(fakeClient(func(ctx context.Context, _ ai.Request, emit func(ai.Event) error) (ai.Response, error) {
		defer close(done)
		if err := emit(ai.Event{Type: "text_delta", Delta: "start"}); err != nil {
			return ai.Response{}, err
		}
		<-ctx.Done()
		return ai.Response{}, ctx.Err()
	}), "alias")
	ctx, cancel := context.WithCancel(context.Background())
	reader, _ := a.Stream(ctx, nil)
	if _, err := reader.Recv(); err != nil {
		t.Fatal(err)
	}
	cancel()
	reader.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("provider read leaked after cancel")
	}
}
func TestRejectUnsupportedContentAndOptions(t *testing.T) {
	a, _ := NewAdapter(fakeClient(func(context.Context, ai.Request, func(ai.Event) error) (ai.Response, error) {
		t.Fatal("unsupported input reached provider")
		return ai.Response{}, nil
	}), "alias")
	if _, err := a.Generate(context.Background(), []*schema.AgenticMessage{{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.UserInputAudio{URL: "audio"})}}}); err == nil {
		t.Fatal("audio silently dropped")
	}
	if _, err := a.Generate(context.Background(), nil, model.WithDeferredTools([]*schema.ToolInfo{{Name: "deferred"}})); err == nil {
		t.Fatal("deferred tool silently dropped")
	}
}

func TestStreamRetainsStateOnlyReasoningBlock(t *testing.T) {
	opaque := json.RawMessage(`{"protocol":"anthropic","value":{"redacted":"opaque"}}`)
	a, _ := NewAdapter(fakeClient(func(_ context.Context, _ ai.Request, emit func(ai.Event) error) (ai.Response, error) {
		if err := emit(ai.Event{Type: "text_delta", Index: 1, Delta: "visible"}); err != nil {
			return ai.Response{}, err
		}
		return ai.Response{Message: ai.Message{Role: "assistant", Content: []ai.Block{{Type: "reasoning", ProviderState: opaque}, {Type: "text", Text: "visible"}}}}, nil
	}), "alias")
	reader, _ := a.Stream(context.Background(), nil)
	message, err := drain(reader)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := MessagesToAI([]*schema.AgenticMessage{message})
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical[0].Content) != 2 || string(canonical[0].Content[0].ProviderState) != string(opaque) || canonical[0].Content[1].Text != "visible" {
		t.Fatalf("redacted reasoning lost: %+v", canonical)
	}
}
