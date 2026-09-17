package agentruntime

import (
	"context"

	"easygo-agent/internal/agent/telemetry"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

func debugSemanticEvent(ctx context.Context, event Event) {
	if event.Kind == EventTextDelta || event.Kind == EventReasoningDelta {
		return
	}
	telemetry.Logger(ctx).Debug("agent semantic event", zap.Any("event", event))
}
func debugAgenticMessage(ctx context.Context, message *schema.AgenticMessage) {
	telemetry.Logger(ctx).Debug("agentic message", zap.Any("message", message))
}
func debugEinoEvent(ctx context.Context, event *adk.TypedAgentEvent[*schema.AgenticMessage]) {
	if entry := telemetry.Logger(ctx).Check(zap.DebugLevel, "eino agent event"); entry != nil {
		entry.Write(zap.Any("event", snapshotEinoEvent(event)))
	}
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
