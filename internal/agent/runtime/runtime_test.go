package agentruntime

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

// TestProjectMessageEmitsReasoningAndAggregatedToolDetails 验证 reasoning 即时投影，tool 参数聚合后再发出。
func TestProjectMessageEmitsReasoningAndAggregatedToolDetails(t *testing.T) {
	run := &agentRun{
		startedTools:  make(map[string]struct{}),
		finishedTools: make(map[string]struct{}),
	}
	run.projectMessage(reasoningChunk(0, "The "))
	run.projectMessage(reasoningChunk(0, "user"))
	run.projectMessage(toolCallChunk(1, "c1", "calculator", ""))
	run.projectMessage(toolCallChunk(1, "", "", `{"operation":"multiply"}`))

	if len(run.pending) != 2 {
		t.Fatalf("pending before tool flush = %d, want 2", len(run.pending))
	}
	if run.pending[0].Kind != EventReasoningDelta || run.pending[0].Text != "The " {
		t.Fatalf("first reasoning event = %+v", run.pending[0])
	}
	if run.pending[1].Kind != EventReasoningDelta || run.pending[1].Text != "user" {
		t.Fatalf("second reasoning event = %+v", run.pending[1])
	}

	run.flushToolDraft()
	if len(run.pending) != 3 {
		t.Fatalf("pending after tool flush = %d, want 3", len(run.pending))
	}
	started := run.pending[2]
	if started.Kind != EventToolStarted || started.Tool != "calculator" || started.CallID != "c1" || started.Arguments != `{"operation":"multiply"}` {
		t.Fatalf("tool started = %+v", started)
	}

	run.projectMessage(&schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{{
			Type: schema.ContentBlockTypeFunctionToolResult,
			FunctionToolResult: &schema.FunctionToolResult{
				CallID: "c1",
				Name:   "calculator",
				Content: []*schema.FunctionToolResultContentBlock{{
					Type: schema.FunctionToolResultContentBlockTypeText,
					Text: &schema.UserInputText{Text: `{"result":42}`},
				}},
			},
		}},
	})
	if len(run.pending) != 4 {
		t.Fatalf("pending after tool result = %d, want 4", len(run.pending))
	}
	finished := run.pending[3]
	if finished.Kind != EventToolFinished || finished.Tool != "calculator" || finished.CallID != "c1" || finished.Result != `{"result":42}` {
		t.Fatalf("tool finished = %+v", finished)
	}
}
