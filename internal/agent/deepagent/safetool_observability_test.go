package deepagent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"easygo-agent/internal/agent/telemetry"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestStreamingToolReportsRecoveryAndCancellation(t *testing.T) {
	for _, mode := range []string{"completed", "start_error", "stream_error", "canceled", "nil_stream"} {
		t.Run(mode, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			ctx, root := telemetry.StartRun(telemetry.WithLogger(context.Background(), zap.New(core)), telemetry.Identity{RunID: "r"})
			defer root.Finish("completed", nil)
			failure := errors.New("tool unavailable")
			endpoint := func(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
				if mode == "start_error" {
					return nil, failure
				}
				if mode == "nil_stream" {
					return nil, nil
				}
				r, w := schema.Pipe[string](2)
				w.Send("first", nil)
				switch mode {
				case "stream_error":
					w.Send("", failure)
				case "canceled":
					w.Send("", context.Canceled)
				}
				w.Close()
				return r, nil
			}
			call, err := newSafeToolMiddleware().WrapStreamableToolCall(ctx, endpoint, &adk.ToolContext{Name: "lookup"})
			if err != nil {
				t.Fatal(err)
			}
			r, err := call(ctx, "private input")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			var output strings.Builder
			var end error
			for {
				chunk, err := r.Recv()
				if err != nil {
					end = err
					break
				}
				output.WriteString(chunk)
			}
			status := "recovered"
			switch mode {
			case "completed":
				status = "completed"
				if output.String() != "first" {
					t.Fatal(output.String())
				}
			case "canceled":
				status = "canceled"
				if !errors.Is(end, context.Canceled) {
					t.Fatal(end)
				}
			default:
				if !strings.Contains(output.String(), "[tool error]") {
					t.Fatal(output.String())
				}
			}
			if mode != "canceled" && !errors.Is(end, io.EOF) {
				t.Fatal(end)
			}
			entries := logs.FilterMessage("agent.phase").FilterField(zap.String("phase", "tool")).FilterField(zap.String("event", "finished")).All()
			if len(entries) != 1 || entries[0].ContextMap()["status"] != status {
				t.Fatalf("status=%s entries=%v", status, entries)
			}
		})
	}
}
