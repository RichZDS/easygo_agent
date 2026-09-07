package logger

import (
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// StreamChunkLog 在内存中收集流式 AgenticMessage chunk，结束后再聚合打印。
type StreamChunkLog struct {
	chunks []*schema.AgenticMessage
}

// Append 收集一条流式 chunk，不立即打印。
func (chunkLog *StreamChunkLog) Append(message *schema.AgenticMessage) {
	if message == nil {
		return
	}
	chunkLog.chunks = append(chunkLog.chunks, message)
}

// Flush 把已收集的 Streaming chunk 聚合成完整 AgenticMessage 后一次性打印。
func (chunkLog *StreamChunkLog) Flush() {
	chunks := chunkLog.chunks
	chunkLog.chunks = nil
	if len(chunks) == 0 {
		return
	}
	concatenated, err := schema.ConcatAgenticMessages(chunks)
	if err != nil {
		wrappedErr := fmt.Errorf("concat agentic message chunks: %w", err)
		Error("concat agentic message chunks failed", zap.Error(wrappedErr), zap.Int("chunk_count", len(chunks)))
		return
	}
	DebugAgenticMessage(concatenated)
}

// einoEventSnapshot 是 TypedAgentEvent 的可序列化快照，避免把消息流本身写入日志。
type einoEventSnapshot struct {
	AgentName string              `json:"agent_name"`
	RunPath   []string            `json:"run_path,omitempty"`
	Err       string              `json:"error,omitempty"`
	Action    *adk.AgentAction    `json:"action,omitempty"`
	Output    *einoOutputSnapshot `json:"output,omitempty"`
}

// einoOutputSnapshot 是 TypedAgentOutput 的可序列化快照。
type einoOutputSnapshot struct {
	CustomizedOutput any                        `json:"customized_output,omitempty"`
	MessageOutput    *einoMessageOutputSnapshot `json:"message_output,omitempty"`
}

// einoMessageOutputSnapshot 是 TypedMessageVariant 的可序列化快照，含完整 AgenticMessage。
type einoMessageOutputSnapshot struct {
	IsStreaming      bool                   `json:"is_streaming"`
	HasMessageStream bool                   `json:"has_message_stream"`
	AgenticRole      schema.AgenticRoleType `json:"agentic_role,omitempty"`
	Role             schema.RoleType        `json:"role,omitempty"`
	ToolName         string                 `json:"tool_name,omitempty"`
	Message          *schema.AgenticMessage `json:"message,omitempty"`
}

// DebugAgenticMessage 以 debug 级别打印完整 AgenticMessage，含对话与 tool 内容块。
func DebugAgenticMessage(message *schema.AgenticMessage) {
	Debug("agentic message", zap.Any("message", message))
}

// DebugEinoEvent 以 debug 级别打印完整 Eino 运行事件。
func DebugEinoEvent(event *adk.TypedAgentEvent[*schema.AgenticMessage]) {
	Debug("eino agent event", zap.Any("event", snapshotEinoEvent(event)))
}

// snapshotEinoEvent 把 Eino 事件转成可写入日志的快照。
func snapshotEinoEvent(event *adk.TypedAgentEvent[*schema.AgenticMessage]) einoEventSnapshot {
	if event == nil {
		return einoEventSnapshot{}
	}
	snapshot := einoEventSnapshot{
		AgentName: event.AgentName,
		RunPath:   runPathNames(event.RunPath),
		Action:    event.Action,
	}
	if event.Err != nil {
		snapshot.Err = event.Err.Error()
	}
	if event.Output == nil {
		return snapshot
	}
	snapshot.Output = &einoOutputSnapshot{CustomizedOutput: event.Output.CustomizedOutput}
	if event.Output.MessageOutput == nil {
		return snapshot
	}
	messageOutput := event.Output.MessageOutput
	snapshot.Output.MessageOutput = &einoMessageOutputSnapshot{
		IsStreaming:      messageOutput.IsStreaming,
		HasMessageStream: messageOutput.MessageStream != nil,
		AgenticRole:      messageOutput.AgenticRole,
		Role:             messageOutput.Role,
		ToolName:         messageOutput.ToolName,
		Message:          messageOutput.Message,
	}
	return snapshot
}

// runPathNames 提取 RunPath 中的 Agent 名称。
func runPathNames(path []adk.RunStep) []string {
	if len(path) == 0 {
		return nil
	}
	names := make([]string, 0, len(path))
	for _, step := range path {
		names = append(names, step.String())
	}
	return names
}
