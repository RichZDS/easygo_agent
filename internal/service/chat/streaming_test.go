package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseProviderSSE(t *testing.T) {
	input := strings.Join([]string{
		": keep-alive",
		"",
		`data: {"choices":[{"delta":{"content":"你"},"finish_reason":null}],"usage":null}`,
		"",
		`data: {"choices":[{"delta":{"content":"好"},"finish_reason":null}],"usage":null}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var deltas []ProviderDelta
	result, sawData, err := parseProviderSSE(context.Background(), strings.NewReader(input), func(delta ProviderDelta) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatalf("parseProviderSSE() error = %v", err)
	}
	if !sawData {
		t.Fatal("parseProviderSSE() did not report content")
	}
	if got := deltas[0].Content + deltas[1].Content; got != "你好" {
		t.Fatalf("combined content = %q", got)
	}
	if result.FinishReason != "stop" {
		t.Fatalf("finish reason = %q", result.FinishReason)
	}
	if !result.HasUsage || result.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v, has usage = %v", result.Usage, result.HasUsage)
	}
}

func TestParseProviderSSEHandlesFragmentedReader(t *testing.T) {
	reader := io.MultiReader(
		strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hel"),
		strings.NewReader("lo\"},\"finish_reason\":null}]}\r\n\r\n"),
		strings.NewReader("data: [DONE]\r\n\r\n"),
	)
	var content string
	_, _, err := parseProviderSSE(context.Background(), reader, func(delta ProviderDelta) error {
		content += delta.Content
		return nil
	})
	if err != nil {
		t.Fatalf("parseProviderSSE() error = %v", err)
	}
	if content != "hello" {
		t.Fatalf("content = %q", content)
	}
}

func TestParseProviderSSERejectsMalformedChunk(t *testing.T) {
	_, _, err := parseProviderSSE(context.Background(), strings.NewReader("data: {oops}\n\n"), func(ProviderDelta) error {
		return nil
	})
	if err == nil {
		t.Fatal("parseProviderSSE() expected an error")
	}
}

func TestHTTPProviderStreamer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload["stream"] != true {
			t.Errorf("stream = %#v", payload["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	streamer := NewHTTPProviderStreamer(server.Client())
	var content string
	result, err := streamer.Stream(context.Background(), ProviderStreamRequest{
		BaseURL: server.URL,
		APIKey:  "secret",
		Model:   "test-model",
		Messages: []ProviderMessage{
			{Role: "user", Content: "hello"},
		},
		MaxTokens: 100,
	}, func(delta ProviderDelta) error {
		content += delta.Content
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if content != "ok" || result.FinishReason != "stop" {
		t.Fatalf("content = %q, result = %#v", content, result)
	}
}

func TestHTTPProviderStreamerRejectsNonSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := NewHTTPProviderStreamer(server.Client()).Stream(context.Background(), ProviderStreamRequest{
		BaseURL: server.URL,
		APIKey:  "bad",
		Model:   "test-model",
	}, func(ProviderDelta) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Stream() error = %v", err)
	}
}

func TestHTTPProviderStreamerCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := NewHTTPProviderStreamer(server.Client()).Stream(ctx, ProviderStreamRequest{
			BaseURL: server.URL,
			APIKey:  "secret",
			Model:   "test-model",
		}, func(ProviderDelta) error { return nil })
		errCh <- err
	}()
	<-started
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stream() did not stop after cancellation")
	}
}

func TestCancellationCoordinator(t *testing.T) {
	coordinator := NewCancellationCoordinator()
	var calls atomic.Int32
	unregister := coordinator.Register("turn-1", func() { calls.Add(1) })
	if !coordinator.Cancel("turn-1") {
		t.Fatal("Cancel() did not find running turn")
	}
	if calls.Load() != 1 {
		t.Fatalf("cancel calls = %d", calls.Load())
	}
	unregister()
	if coordinator.Cancel("turn-1") {
		t.Fatal("Cancel() found an unregistered turn")
	}
}

func TestNewCompletionChunk(t *testing.T) {
	chunk := newCompletionChunk("turn-1", "model-a", "hello", "", nil)
	if chunk.Object != "chat.completion.chunk" || chunk.ID != "turn-1" {
		t.Fatalf("chunk = %#v", chunk)
	}
	if got := chunk.Choices[0].Delta["content"]; got != "hello" {
		t.Fatalf("content = %#v", got)
	}
	if chunk.Choices[0].FinishReason != nil {
		t.Fatalf("finish reason = %#v", chunk.Choices[0].FinishReason)
	}
}
