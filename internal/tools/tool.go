package tools

import (
	"context"

	"easygo-agent/internal/maze"
	"easygo-agent/internal/skill"
	"easygo-agent/internal/toolregistry"

	"github.com/cloudwego/eino/components/tool"
)

// AgentTool 集中构造并持有当前 Agent 可用的全部 Tool。
type AgentTool struct {
	sandbox    *SandboxControllerConfig
	workshop   *WorkshopConfig
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

// WithWorkshop connects independently operated CLI workers to the native loop.
func (t *AgentTool) WithWorkshop(cfg WorkshopConfig) *AgentTool {
	t.workshop = &cfg
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
	r, err := t.Registry(ctx)
	if err != nil {
		return nil, err
	}
	return r.Native(ctx)
}

// Registry is the single registration point for native and skill-disclosed tools.
func (t *AgentTool) Registry(ctx context.Context) (*toolregistry.Registry, error) {
	r := toolregistry.New()
	add := func(items []tool.BaseTool, group string, hidden bool) error {
		for _, item := range items {
			info, err := item.Info(ctx)
			if err != nil {
				return err
			}
			retry := toolregistry.Unsafe
			switch info.Name {
			case "calculator", "load_skill", "list_skills", "read_skill_resource", "listmaze", "detailmaze", "runmaze", "workshop_catalog", "workshop_get", "workshop_list", "workshop_result":
				retry = toolregistry.ReadOnly
			case "workshop_submit", "workshop_cancel":
				retry = toolregistry.Idempotent
			}
			if err = r.Register(ctx, item, group, retry, hidden); err != nil {
				return err
			}
		}
		return nil
	}
	calculator, err := NewCalculator()
	if err != nil {
		return nil, err
	}
	if err = add([]tool.BaseTool{calculator}, "calculation", false); err != nil {
		return nil, err
	}
	if t.skills != nil {
		items, err := NewSkillTools(t.skills)
		if err != nil {
			return nil, err
		}
		if err = add(items, "skills", false); err != nil {
			return nil, err
		}
	}
	if t.sandbox != nil {
		items, err := NewSandboxTools(*t.sandbox)
		if err != nil {
			return nil, err
		}
		if err = add(items, "sandbox", false); err != nil {
			return nil, err
		}
	}
	if t.workshop != nil {
		items, err := NewWorkshopTools(*t.workshop)
		if err != nil {
			return nil, err
		}
		if err = add(items, "workshop", false); err != nil {
			return nil, err
		}
	}
	if t.mazeStore != nil && (t.boundMaze || t.hiddenMaze) {
		items, err := NewMazeTools(t.mazeStore)
		if err != nil {
			return nil, err
		}
		if err = add(items, "maze", t.hiddenMaze); err != nil {
			return nil, err
		}
	}
	return r, nil
}
