package telemetry

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

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
}

func (s *modelStats) observe(message *schema.AgenticMessage) {
	s.chunks++
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.TokenUsage == nil {
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
	if s.usageKnown {
		fields = append(fields, zap.Int("prompt_tokens", s.prompt), zap.Int("completion_tokens", s.completion), zap.Int("total_tokens", s.total))
	}
	return fields
}
