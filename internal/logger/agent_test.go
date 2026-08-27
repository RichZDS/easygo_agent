package logger

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestStreamChunkLogFlushPrintsConcatenatedMessageOnce 验证 reasoning、tool arguments、assistant text 聚合后只打印一次。
func TestStreamChunkLogFlushPrintsConcatenatedMessageOnce(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()

	var chunkLog StreamChunkLog
	chunkLog.Append(reasoningChunk(0, "The "))
	chunkLog.Append(reasoningChunk(0, "user"))
	chunkLog.Append(toolCallChunk(1, "c1", "calculator", ""))
	chunkLog.Append(toolCallChunk(1, "", "", `{"operation":"multiply"}`))
	chunkLog.Flush()

	entries := observed.FilterMessage("agentic message").All()
	if len(entries) != 1 {
		t.Fatalf("got %d agentic message logs, want 1", len(entries))
	}
	first, ok := entries[0].ContextMap()["message"].(string)
	if !ok {
		t.Fatalf("logged message type = %T, want string", entries[0].ContextMap()["message"])
	}
	if !strings.Contains(first, "The user") {
		t.Fatalf("concatenated reasoning missing from log: %s", first)
	}
	if !strings.Contains(first, `{"operation":"multiply"}`) {
		t.Fatalf("concatenated tool arguments missing from log: %s", first)
	}

	chunkLog.Append(assistantTextChunk(0, "123"))
	chunkLog.Append(assistantTextChunk(0, " × 456"))
	chunkLog.Flush()

	entries = observed.FilterMessage("agentic message").All()
	if len(entries) != 2 {
		t.Fatalf("got %d agentic message logs after second flush, want 2", len(entries))
	}
	second, ok := entries[1].ContextMap()["message"].(string)
	if !ok {
		t.Fatalf("second logged message type = %T, want string", entries[1].ContextMap()["message"])
	}
	if !strings.Contains(second, "123 × 456") {
		t.Fatalf("concatenated assistant text missing from log: %s", second)
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

// assistantTextChunk 构造一条 assistant_gen_text 流式 chunk。
func assistantTextChunk(index int, text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{{
			Type:             schema.ContentBlockTypeAssistantGenText,
			AssistantGenText: &schema.AssistantGenText{Text: text},
			StreamingMeta:    &schema.StreamingMeta{Index: index},
		}},
	}
}
