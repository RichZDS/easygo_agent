package agentruntime

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/usermemory"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

func (run *agentRun) initialize() error {
	if run.queueLease == nil {
		return ErrStoreUnavailable
	}
	run.inputMessages = append(slices.Clone(run.queueLease.Messages()), conversation.RunInput(run.record))
	recallCtx, recallSpan := telemetry.Start(run.context, "memory", "recall", zap.Bool("enabled", run.memory != nil))
	memoryContext, recallErr := recallMemoryPrompt(recallCtx, run.memory, run.record.Username)
	recallSpan.Finish("", recallErr, zap.Int("memory_bytes", len(memoryContext)))
	if recallErr != nil {
		return recallErr
	}
	modelInput := make([]*schema.AgenticMessage, 0, len(run.inputMessages)+1)
	if memoryContext != "" {
		modelInput = append(modelInput, schema.SystemAgenticMessage(memoryContext))
	}
	modelInput = append(modelInput, DropPersistedSystemMessages(run.inputMessages)...)
	if run.record.Source != "" {
		modelInput = append(modelInput, conversation.RunInput(run.record))
	}
	if summary, _ := run.context.Value(taskSummaryKey{}).(string); summary != "" {
		modelInput = append([]*schema.AgenticMessage{schema.SystemAgenticMessage(summary)}, modelInput...)
	}
	if run.agent == nil {
		return ErrAgentUnavailable
	}
	run.iterator = run.agent.Run(run.context, &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: modelInput, EnableStreaming: true})
	if run.iterator == nil {
		return ErrAgentUnavailable
	}
	return nil
}

// DropPersistedSystemMessages removes system rows from stored context before
// the next Run. Deep Agent re-injects Instruction; leftover system messages
// would duplicate it. Extracted theme notes must therefore be user messages.
func DropPersistedSystemMessages(messages []*schema.AgenticMessage) []*schema.AgenticMessage {
	out := make([]*schema.AgenticMessage, 0, len(messages))
	for _, message := range messages {
		if message != nil && message.Role != schema.AgenticRoleTypeSystem {
			out = append(out, message)
		}
	}
	return out
}

func recallMemoryPrompt(ctx context.Context, store conversation.MemoryStore, username string) (string, error) {
	if store == nil || strings.TrimSpace(username) == "" {
		return "", nil
	}
	memories, err := store.Recall(ctx, username, conversation.MaxProfileMemories)
	if err != nil {
		return "", fmt.Errorf("recall user memory: %w", err)
	}
	return usermemory.Prompt(memories), nil
}
