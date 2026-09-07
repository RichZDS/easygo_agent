package agentruntime

import (
	"easygo-agent/internal/logger"

	"go.uber.org/zap"
)

// debugSemanticEvent 以 debug 级别打印完整语义事件。文本与 reasoning 增量已聚合进 AgenticMessage，不再逐条打印。
func debugSemanticEvent(event Event) {
	if event.Kind == EventTextDelta || event.Kind == EventReasoningDelta {
		return
	}
	logger.Debug("agent semantic event", zap.Any("event", event))
}
