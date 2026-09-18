package tools

import (
	"context"
	"fmt"

	"easygo-agent/internal/logger"
	"easygo-agent/internal/skill"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"go.uber.org/zap"
)

// LoadSkillInput names one catalog entry. The name must match a skills/
// directory exactly; path fragments are rejected by the library.
type LoadSkillInput struct {
	Name string `json:"name" jsonschema:"required,description=Exact catalog skill name to load into context"`
}

// LoadSkillOutput is the specialized skill body. The catalog heuristic stays
// in the system prompt; this is the only way that body enters the run.
type LoadSkillOutput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// NewLoadSkill exposes skill.LoadSkill as the agent-facing load_skill tool.
func NewLoadSkill(lib *skill.Library) (tool.InvokableTool, error) {
	if lib == nil {
		return nil, fmt.Errorf("skill library is required")
	}
	load := func(_ context.Context, input LoadSkillInput) (LoadSkillOutput, error) {
		content, err := lib.LoadSkill(input.Name)
		if err != nil {
			logger.Error("load_skill failed", zap.String("name", input.Name), zap.Error(err))
			return LoadSkillOutput{}, err
		}
		return LoadSkillOutput{Name: input.Name, Content: content}, nil
	}
	item, err := utils.InferTool(
		"load_skill",
		"Load one specialized skill from the catalog into this run. Call this before following a catalog entry. Pass the exact Name from the catalog Directory.",
		load,
	)
	if err != nil {
		return nil, fmt.Errorf("infer load_skill tool schema: %w", err)
	}
	return item, nil
}

func NewSkillTools(lib *skill.Library) ([]tool.BaseTool, error) {
	load, err := NewLoadSkill(lib)
	if err != nil {
		return nil, err
	}
	list, err := utils.InferTool("list_skills", "Discover skills by name or description; empty query lists all skills, including after compression.", func(ctx context.Context, in struct {
		Query string `json:"query"`
	}) ([]skill.Entry, error) {
		return lib.List(in.Query), nil
	})
	if err != nil {
		return nil, err
	}
	read, err := utils.InferTool("read_skill_resource", "Read a UTF-8 reference inside a loaded skill directory (maximum 64 KiB).", func(ctx context.Context, in struct {
		Name string `json:"name" jsonschema:"required"`
		Path string `json:"path" jsonschema:"required"`
	}) (LoadSkillOutput, error) {
		content, err := lib.ReadResource(in.Name, in.Path)
		return LoadSkillOutput{Name: in.Name, Content: content}, err
	})
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{load, list, read}, nil
}
