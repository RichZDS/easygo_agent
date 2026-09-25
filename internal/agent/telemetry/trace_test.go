package telemetry_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/testutil"
	"easygo-agent/pkg/ai"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func observed(t *testing.T) (context.Context, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.InfoLevel)
	return telemetry.WithLogger(context.Background(), zap.New(core)), logs
}

func TestConcurrentExecutionsHaveIndependentIdentityAndOrderedPhases(t *testing.T) {
	ctx, logs := observed(t)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			ctx, root := telemetry.StartRun(ctx, telemetry.Identity{SessionID: "session", RunID: "retried-run"})
			var phases sync.WaitGroup
			for range 8 {
				phases.Go(func() {
					_, span := telemetry.Start(ctx, "tool", "calculator")
					span.Finish("", nil)
					span.Finish("failed", errors.New("ignored duplicate"))
				})
			}
			phases.Wait()
			root.Finish("completed", nil)
		})
	}
	wg.Wait()
	sequences := map[string]uint64{}
	finished := 0
	for _, entry := range logs.All() {
		f := entry.ContextMap()
		id := f["execution_id"].(string)
		if f["run_id"] != "retried-run" || f["session_id"] != "session" {
			t.Fatalf("missing identity: %v", f)
		}
		seq := f["sequence"].(uint64)
		if seq != sequences[id]+1 {
			t.Fatalf("unordered trace: %v", f)
		}
		sequences[id] = seq
		if f["phase"] == "run" && f["event"] == "finished" {
			finished++
			if f["tool_calls"] != int64(8) {
				t.Fatalf("wrong count: %v", f)
			}
		}
	}
	if len(sequences) != 2 || finished != 2 || logs.Len() != 36 {
		t.Fatalf("traces=%v finished=%d events=%d", sequences, finished, logs.Len())
	}
}

func TestModelStreamPreservesMessagesAndCountsCumulativeUsageOnce(t *testing.T) {
	ctx, logs := observed(t)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx, root := telemetry.StartRun(ctx, telemetry.Identity{RunID: "r", SessionID: "s"})
	first, last := testutil.Text("private first"), testutil.Text("private last")
	first.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}}
	last.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}}
	first.ResponseMeta.Extension = map[string]any{"cost": ai.Cost{Known: true, Currency: "USD", Amount: 0.3}}
	last.ResponseMeta.Extension = map[string]any{
		"model": "configured-alias", "id": "response-1",
		"cost":  map[string]any{"known": true, "currency": "USD", "amount": 0.5},
		"usage": ai.Usage{Known: true, InputTokens: 10, OutputTokens: 2, CacheReadTokens: 4, CacheWriteTokens: 2},
	}
	input := []*schema.AgenticMessage{schema.UserAgenticMessage("private prompt")}
	m := telemetry.Model(&testutil.Model{StreamFunc: func(_ context.Context, in []*schema.AgenticMessage) (*schema.StreamReader[*schema.AgenticMessage], error) {
		if in[0] != input[0] {
			t.Fatal("rewrote input")
		}
		return schema.StreamReaderFromArray([]*schema.AgenticMessage{first, last}), nil
	}}, "main")
	reader, err := m.Stream(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, want := range []*schema.AgenticMessage{first, last} {
		got, err := reader.Recv()
		if err != nil || got != want {
			t.Fatalf("changed stream: %v %v", got, err)
		}
	}
	if _, err := reader.Recv(); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	cancel()
	root.Finish("completed", nil)
	entries := logs.FilterMessage("agent.phase").All()
	var models int
	for _, entry := range entries {
		f := entry.ContextMap()
		if _, exists := f["message"]; exists {
			t.Fatal("logged message payload")
		}
		if f["phase"] == "model" && f["event"] == "finished" {
			models++
			if f["status"] != "completed" || f["total_tokens"] != int64(12) || f["chunks"] != int64(2) || f["usage_reported"] != true {
				t.Fatalf("wrong usage/status: %v", f)
			}
			if _, ok := f["first_chunk_ms"]; !ok {
				t.Fatal("missing first chunk latency")
			}
			if f["cost_known"] != true || f["cost_amount"] != 0.5 || f["cost_currency"] != "USD" || f["cache_read_tokens"] != int64(4) || f["cache_write_tokens"] != int64(2) || f["model_alias"] != "configured-alias" {
				t.Fatalf("lost or double-counted gateway accounting: %v", f)
			}
		}
	}
	if models != 1 {
		t.Fatalf("model finishes=%d", models)
	}
}

type recordingSink struct {
	events []telemetry.PhaseEvent
	err    error
}

func (r *recordingSink) PersistPhase(_ context.Context, event telemetry.PhaseEvent) error {
	r.events = append(r.events, event)
	return r.err
}

func TestNilPhaseSinkLeavesFinishedLogUnchanged(t *testing.T) {
	ctx, logs := observed(t)
	_, root := telemetry.StartRun(ctx, telemetry.Identity{RunID: "r", SessionID: "s"})
	root.Finish("completed", nil)
	finished := logs.FilterMessage("agent.phase").FilterField(zap.String("phase", "run")).FilterField(zap.String("event", "finished")).All()
	if len(finished) != 1 {
		t.Fatalf("finishes=%d", len(finished))
	}
	if _, ok := finished[0].ContextMap()["phase_persist_failures"]; ok {
		t.Fatalf("nil sink changed the finished log: %v", finished[0].ContextMap())
	}
}

func TestPhaseSinkErrorsDoNotFailTheRun(t *testing.T) {
	ctx, logs := observed(t)
	secret := "private prompt body"
	sink := &recordingSink{err: errors.New("phase store unavailable")}
	ctx = telemetry.WithPhaseSink(ctx, sink)
	ctx, root := telemetry.StartRun(ctx, telemetry.Identity{RunID: "r", SessionID: "s"}, zap.String("prompt", secret), zap.String("message", secret))
	_, child := telemetry.Start(ctx, "tool", "calculator", zap.String("arguments", secret))
	child.Finish("completed", nil)
	root.Finish("completed", nil)
	finished := logs.FilterMessage("agent.phase").FilterField(zap.String("phase", "run")).FilterField(zap.String("event", "finished")).All()
	if len(finished) != 1 {
		t.Fatalf("finishes=%d", len(finished))
	}
	f := finished[0].ContextMap()
	if f["status"] != "completed" || f["prompt"] != secret {
		t.Fatalf("run log=%v", f)
	}
	failures, ok := f["phase_persist_failures"].(int64)
	if !ok || failures <= 0 {
		t.Fatalf("phase_persist_failures=%v (%T)", f["phase_persist_failures"], f["phase_persist_failures"])
	}
	if len(sink.events) != 4 {
		t.Fatalf("events=%d", len(sink.events))
	}
	for _, event := range sink.events {
		text := event.Phase + event.Name + event.Event + event.Status + event.Error + event.RunID + event.ExecutionID
		if strings.Contains(text, secret) {
			t.Fatalf("sink stored prompt text: %+v", event)
		}
	}
}

func TestModelErrorsAndUnconsumedCancellationFinishOnce(t *testing.T) {
	for _, mode := range []string{"generate", "start", "stream", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, logs := observed(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			ctx, root := telemetry.StartRun(ctx, telemetry.Identity{RunID: "r", SessionID: "s"})
			failure := errors.New("provider unavailable")
			m := telemetry.Model(&testutil.Model{
				GenerateFunc: func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error) { return nil, failure },
				StreamFunc: func(context.Context, []*schema.AgenticMessage) (*schema.StreamReader[*schema.AgenticMessage], error) {
					if mode == "start" {
						return nil, failure
					}
					r, w := schema.Pipe[*schema.AgenticMessage](1)
					if mode == "stream" {
						w.Send(nil, failure)
					}
					w.Close()
					return r, nil
				},
			}, "main")
			if mode == "generate" {
				if _, err := m.Generate(ctx, nil); err != failure {
					t.Fatal(err)
				}
			} else {
				r, err := m.Stream(ctx, nil)
				if mode == "start" {
					if err != failure {
						t.Fatal(err)
					}
				} else {
					defer r.Close()
					if mode == "stream" {
						if _, err = r.Recv(); err != failure {
							t.Fatal(err)
						}
					}
				}
			}
			cancel()
			deadline := time.Now().Add(time.Second)
			for logs.FilterField(zap.String("phase", "model")).FilterField(zap.String("event", "finished")).Len() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			root.Finish("failed", failure)
			finished := logs.FilterField(zap.String("phase", "model")).FilterField(zap.String("event", "finished")).All()
			if len(finished) != 1 {
				t.Fatalf("finishes=%d", len(finished))
			}
			f := finished[0].ContextMap()
			want := "failed"
			if mode == "cancel" {
				want = "canceled"
			}
			if f["status"] != want {
				t.Fatalf("status: %v", f)
			}
			if f["chunks"] != int64(0) || f["usage_reported"] != false {
				t.Fatalf("invented response chunks or usage: %v", f)
			}
			if _, ok := f["total_tokens"]; ok {
				t.Fatalf("invented usage: %v", f)
			}
		})
	}
}
