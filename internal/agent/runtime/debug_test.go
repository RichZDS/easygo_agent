package agentruntime

import (
	"testing"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestDebugSemanticEventSkipsTextDelta 验证文本与 reasoning 增量不单独打印。
func TestDebugSemanticEventSkipsTextDelta(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()

	debugSemanticEvent(Event{Kind: EventTextDelta, Text: "123"})
	debugSemanticEvent(Event{Kind: EventReasoningDelta, Text: "think"})
	debugSemanticEvent(Event{Kind: EventCompleted, Text: "123 × 456"})

	if got := observed.FilterMessage("agent semantic event").Len(); got != 1 {
		t.Fatalf("got %d semantic event logs, want 1", got)
	}
}

// reasoningChunk 构造一条 reasoning 流式 chunk。
func reasoningChunk(index int, text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{{
			Type:          schema.ContentBlockTypeReasoning,
			Reasoning:     &schema.Reasoning{Text: text},
			StreamingMeta: &schema.StreamingMeta{Index: index},
		}},
	}
}

// toolCallChunk 构造一条 function_tool_call 流式 chunk。
func toolCallChunk(index int, callID, name, arguments string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{{
			Type: schema.ContentBlockTypeFunctionToolCall,
			FunctionToolCall: &schema.FunctionToolCall{
				CallID:    callID,
				Name:      name,
				Arguments: arguments,
			},
			StreamingMeta: &schema.StreamingMeta{Index: index},
		}},
	}
}
