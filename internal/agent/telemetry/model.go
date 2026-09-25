package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"easygo-agent/pkg/ai"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// Model observes actual provider calls, including summary calls. It forwards
// options and message pointers unchanged and never buffers or copies the stream.
func Model(next model.AgenticModel, name string) model.AgenticModel {
	return &observedModel{next: next, name: name}
}

type observedModel struct {
	next model.AgenticModel
	name string
}

func (m *observedModel) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	ctx, span := Start(ctx, "model", m.name, zap.Int("input_messages", len(input)), zap.Bool("streaming", false))
	output, err := m.next.Generate(ctx, input, opts...)
	stats := modelStats{}
	if output != nil {
		stats.observe(output)
	}
	span.Finish("", err, stats.fields()...)
	return output, err
}

func (m *observedModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	ctx, span := Start(ctx, "model", m.name, zap.Int("input_messages", len(input)), zap.Bool("streaming", true))
	started := time.Now()
	stats := modelStats{}
	reader, err := m.next.Stream(ctx, input, opts...)
	if err != nil {
		span.Finish("", err, stats.fields()...)
		return reader, err
	}
	if reader == nil {
		err = errors.New("model returned a nil stream")
		span.Finish("", err, stats.fields()...)
		return nil, err
	}
	if span == nil {
		return reader, nil
	}
	var mu sync.Mutex
	firstChunk := time.Duration(0)
	finish := func(err error) {
		mu.Lock()
		fields := stats.fields()
		if stats.chunks > 0 {
			fields = append(fields, zap.Float64("first_chunk_ms", float64(firstChunk)/float64(time.Millisecond)))
		}
		mu.Unlock()
		span.Finish("", err, fields...)
	}
	stop := context.AfterFunc(ctx, func() { finish(ctx.Err()) })
	return schema.StreamReaderWithConvert(reader, func(chunk *schema.AgenticMessage) (*schema.AgenticMessage, error) {
		mu.Lock()
		if stats.chunks == 0 {
			firstChunk = time.Since(started)
		}
		stats.observe(chunk)
		mu.Unlock()
		return chunk, nil
	}, schema.WithOnEOF(func() (any, error) {
		stop()
		finish(ctx.Err())
		return nil, io.EOF
	}), schema.WithErrWrapper(func(err error) error {
		stop()
		finish(err)
		return err
	})), nil
}

// Match Eino's concatTokenUsage: streamed usage can be cumulative snapshots.
// Taking maxima avoids counting the same provider usage once per chunk.
type modelStats struct {
	chunks     int
	usageKnown bool
	prompt     int
	completion int
	total      int
	cacheRead  int64
	cacheWrite int64
	cost       ai.Cost
	model      string
	responseID string
}

func (s *modelStats) observe(message *schema.AgenticMessage) {
	s.chunks++
	if message == nil || message.ResponseMeta == nil {
		return
	}
	if ext, ok := message.ResponseMeta.Extension.(map[string]any); ok {
		if value, ok := ext["model"].(string); ok {
			s.model = value
		}
		if value, ok := ext["id"].(string); ok {
			s.responseID = value
		}
		// Extension values may be concrete structs during a run or generic maps
		// after JSON persistence. Decode both without logging the extension body.
		if raw, err := json.Marshal(ext["usage"]); err == nil {
			var usage ai.Usage
			if json.Unmarshal(raw, &usage) == nil && usage.Known {
				s.cacheRead = max(s.cacheRead, usage.CacheReadTokens)
				s.cacheWrite = max(s.cacheWrite, usage.CacheWriteTokens)
			}
		}
		if raw, err := json.Marshal(ext["cost"]); err == nil {
			var cost ai.Cost
			if json.Unmarshal(raw, &cost) == nil && cost.Known {
				s.cost = cost // authoritative/cumulative snapshot, never a sum of chunks
			}
		}
	}
	if message.ResponseMeta.TokenUsage == nil {
		return
	}
	u := message.ResponseMeta.TokenUsage
	s.usageKnown = true
	s.prompt = max(s.prompt, u.PromptTokens)
	s.completion = max(s.completion, u.CompletionTokens)
	s.total = max(s.total, u.TotalTokens)
}

func (s *modelStats) fields() []zap.Field {
	fields := []zap.Field{zap.Int("chunks", s.chunks), zap.Bool("usage_reported", s.usageKnown)}
	fields = append(fields, zap.Bool("cost_known", s.cost.Known))
	if s.model != "" {
		fields = append(fields, zap.String("model_alias", s.model))
	}
	if s.responseID != "" {
		fields = append(fields, zap.String("response_id", s.responseID))
	}
	if s.cost.Known {
		fields = append(fields, zap.Float64("cost_amount", s.cost.Amount), zap.String("cost_currency", s.cost.Currency))
	}
	if s.usageKnown {
		fields = append(fields, zap.Int("prompt_tokens", s.prompt), zap.Int("completion_tokens", s.completion), zap.Int("total_tokens", s.total))
		fields = append(fields, zap.Int64("cache_read_tokens", s.cacheRead), zap.Int64("cache_write_tokens", s.cacheWrite))
	}
	return fields
}
