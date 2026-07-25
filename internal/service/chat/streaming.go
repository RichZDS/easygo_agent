package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	redisplatform "easygo-agent/internal/platform/redis"

	redisclient "github.com/redis/go-redis/v9"
)

type ProviderMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ProviderUsage struct {
	PromptTokens     uint32 `json:"prompt_tokens"`
	CompletionTokens uint32 `json:"completion_tokens"`
	TotalTokens      uint32 `json:"total_tokens"`
}

type ProviderStreamRequest struct {
	BaseURL   string
	APIKey    string
	Model     string
	Messages  []ProviderMessage
	MaxTokens uint32
}

type ProviderDelta struct {
	Content      string
	FinishReason string
	Usage        *ProviderUsage
}

type ProviderStreamResult struct {
	FinishReason string
	Usage        ProviderUsage
	HasUsage     bool
}

type ProviderStreamer interface {
	Stream(context.Context, ProviderStreamRequest, func(ProviderDelta) error) (ProviderStreamResult, error)
}

type HTTPProviderStreamer struct {
	client *http.Client
}

func NewHTTPProviderStreamer(client *http.Client) *HTTPProviderStreamer {
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	return &HTTPProviderStreamer{client: client}
}

type providerStreamPayload struct {
	Model         string            `json:"model"`
	Messages      []ProviderMessage `json:"messages"`
	MaxTokens     uint32            `json:"max_tokens"`
	Stream        bool              `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

type providerStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *ProviderUsage `json:"usage"`
}

func (s *HTTPProviderStreamer) Stream(ctx context.Context, input ProviderStreamRequest, emit func(ProviderDelta) error) (ProviderStreamResult, error) {
	var payload providerStreamPayload
	payload.Model = input.Model
	payload.Messages = input.Messages
	payload.MaxTokens = input.MaxTokens
	payload.Stream = true
	payload.StreamOptions.IncludeUsage = true

	body, err := json.Marshal(payload)
	if err != nil {
		return ProviderStreamResult{}, err
	}
	endpoint := strings.TrimRight(input.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ProviderStreamResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+input.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := s.client.Do(req)
	if err != nil {
		return ProviderStreamResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return ProviderStreamResult{}, fmt.Errorf("provider status %d", resp.StatusCode)
	}

	result, sawData, err := parseProviderSSE(ctx, resp.Body, emit)
	if err != nil {
		return ProviderStreamResult{}, err
	}
	if !sawData {
		return ProviderStreamResult{}, errors.New("provider returned no completion")
	}
	if result.FinishReason == "" {
		result.FinishReason = "stop"
	}
	return result, nil
}

func parseProviderSSE(ctx context.Context, reader io.Reader, emit func(ProviderDelta) error) (ProviderStreamResult, bool, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)

	var result ProviderStreamResult
	var dataLines []string
	sawData := false

	flush := func() (bool, error) {
		if len(dataLines) == 0 {
			return false, nil
		}
		raw := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if strings.TrimSpace(raw) == "[DONE]" {
			return true, nil
		}
		var chunk providerStreamChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return false, fmt.Errorf("decode provider stream chunk: %w", err)
		}
		delta := ProviderDelta{Usage: chunk.Usage}
		if chunk.Usage != nil {
			result.Usage = *chunk.Usage
			result.HasUsage = true
		}
		if len(chunk.Choices) > 0 {
			delta.Content = chunk.Choices[0].Delta.Content
			if chunk.Choices[0].FinishReason != nil {
				delta.FinishReason = *chunk.Choices[0].FinishReason
				result.FinishReason = delta.FinishReason
			}
		}
		if delta.Content != "" {
			sawData = true
		}
		if delta.Content != "" || delta.FinishReason != "" || delta.Usage != nil {
			if err := emit(delta); err != nil {
				return false, err
			}
		}
		return false, nil
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return ProviderStreamResult{}, sawData, err
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			done, err := flush()
			if err != nil {
				return ProviderStreamResult{}, sawData, err
			}
			if done {
				return result, sawData, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return ProviderStreamResult{}, sawData, err
	}
	if _, err := flush(); err != nil {
		return ProviderStreamResult{}, sawData, err
	}
	return result, sawData, nil
}

type TurnEventSink interface {
	Emit(context.Context, string, string, any) error
}

type RedisTurnEventSink struct{}

func NewRedisTurnEventSink() *RedisTurnEventSink { return &RedisTurnEventSink{} }

func turnEventStream(turnID string) string { return "easygo:chat:turn:" + turnID + ":events" }

func (s *RedisTurnEventSink) Emit(ctx context.Context, turnID, event string, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := redisplatform.XAdd(ctx, &redisclient.XAddArgs{
		Stream: turnEventStream(turnID),
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{"event": event, "data": string(encoded)},
	}); err != nil {
		return err
	}
	_, err = redisplatform.Expire(ctx, turnEventStream(turnID), time.Hour)
	return err
}

type CancellationCoordinator struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func NewCancellationCoordinator() *CancellationCoordinator {
	return &CancellationCoordinator{running: make(map[string]context.CancelFunc)}
}

func (c *CancellationCoordinator) Register(turnID string, cancel context.CancelFunc) func() {
	c.mu.Lock()
	c.running[turnID] = cancel
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.running, turnID)
		c.mu.Unlock()
	}
}

func (c *CancellationCoordinator) Cancel(turnID string) bool {
	c.mu.Lock()
	cancel := c.running[turnID]
	c.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

type completionChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int            `json:"index"`
		Delta        map[string]any `json:"delta"`
		FinishReason *string        `json:"finish_reason"`
	} `json:"choices"`
	Usage *ProviderUsage `json:"usage,omitempty"`
}

func newCompletionChunk(turnID, modelName, content, finishReason string, usage *ProviderUsage) completionChunk {
	chunk := completionChunk{
		ID:      turnID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   modelName,
		Usage:   usage,
	}
	choice := struct {
		Index        int            `json:"index"`
		Delta        map[string]any `json:"delta"`
		FinishReason *string        `json:"finish_reason"`
	}{
		Index: 0,
		Delta: map[string]any{},
	}
	if content != "" {
		choice.Delta["content"] = content
	}
	if finishReason != "" {
		choice.FinishReason = &finishReason
	}
	chunk.Choices = append(chunk.Choices, choice)
	return chunk
}
