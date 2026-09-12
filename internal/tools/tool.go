package tools

import (
	"context"

	"easygo-agent/internal/logger"
	"easygo-agent/internal/maze"
	"easygo-agent/internal/skill"

	"github.com/cloudwego/eino/components/tool"
	"go.uber.org/zap"
)

// AgentTool 集中构造并持有当前 Agent 可用的全部 Tool。
type AgentTool struct {
	sandbox    *SandboxControllerConfig
	skills     *skill.Library
	mazeStore  *maze.Store
	boundMaze  bool
	hiddenMaze bool
}

// NewAgentTool 创建 Tool 集合，后续新增 Tool 只需在 AllTools 中 append。
func NewAgentTool(sandbox ...SandboxControllerConfig) *AgentTool {
	result := &AgentTool{}
	if len(sandbox) > 0 {
		cfg := sandbox[0]
		result.sandbox = &cfg
	}
	return result
}

// WithSkills registers load_skill. The catalog heuristic is not a tool; it
// belongs in the system prompt. Specialized bodies load only through this tool.
func (t *AgentTool) WithSkills(lib *skill.Library) *AgentTool {
	t.skills = lib
	return t
}

// WithHiddenMaze keeps maze tools off the native list. The agent reaches them
// through call_tool after load_skill.
func (t *AgentTool) WithHiddenMaze(store *maze.Store) *AgentTool {
	t.mazeStore = store
	t.hiddenMaze = true
	t.boundMaze = false
	return t
}

// WithBoundMaze puts listmaze/detailmaze/runmaze/makemaze on the native tool
// list so the model sees their full schemas at startup.
func (t *AgentTool) WithBoundMaze(store *maze.Store) *AgentTool {
	t.mazeStore = store
	t.boundMaze = true
	t.hiddenMaze = false
	return t
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

	if t.sandbox != nil {
		sandboxTools, err := NewSandboxTools(*t.sandbox)
		if err != nil {
			logger.Error("collect all tools failed", zap.String("tool", "sandbox"), zap.Error(err))
			return nil, err
		}
		all = append(all, sandboxTools...)
	}
	if t.skills != nil {
		loadSkill, skillErr := NewLoadSkill(t.skills)
		if skillErr != nil {
			logger.Error("collect all tools failed", zap.String("tool", "load_skill"), zap.Error(skillErr))
			return nil, skillErr
		}
		all = append(all, loadSkill)
	}
	if t.mazeStore != nil && t.boundMaze {
		mazeTools, mazeErr := NewMazeTools(t.mazeStore)
		if mazeErr != nil {
			logger.Error("collect all tools failed", zap.String("tool", "maze"), zap.Error(mazeErr))
			return nil, mazeErr
		}
		all = append(all, mazeTools...)
	}
	if t.mazeStore != nil && t.hiddenMaze {
		hidden, hiddenErr := HiddenMazeTools(t.mazeStore)
		if hiddenErr != nil {
			logger.Error("collect all tools failed", zap.String("tool", "call_tool"), zap.Error(hiddenErr))
			return nil, hiddenErr
		}
		callTool, callErr := NewCallTool(hidden)
		if callErr != nil {
			logger.Error("collect all tools failed", zap.String("tool", "call_tool"), zap.Error(callErr))
			return nil, callErr
		}
		all = append(all, callTool)
	}

	return all, nil
}
