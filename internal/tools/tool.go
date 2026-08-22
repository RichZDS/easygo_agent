package tools

import (
	"context"

	"easygo-agent/internal/logger"

	"github.com/cloudwego/eino/components/tool"
	"go.uber.org/zap"
)

// AgentTool 集中构造并持有当前 Agent 可用的全部 Tool。
type AgentTool struct{}

// NewAgentTool 创建 Tool 集合，后续新增 Tool 只需在 AllTools 中 append。
func NewAgentTool() *AgentTool {
	return &AgentTool{}
}

// AllTools 逐个构造内置 Tool 并收集到切片中，供 Agent 一次性绑定。
func (t *AgentTool) AllTools(ctx context.Context) ([]tool.BaseTool, error) {
	var all []tool.BaseTool

	calculator, err := NewCalculator()
	if err != nil {
		logger.Error("collect all tools failed", zap.String("tool", "calculator"), zap.Error(err))
		return nil, err
	}
	all = append(all, calculator)

	return all, nil
}
