package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"easygo-agent/internal/logger"
	"easygo-agent/internal/maze"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"go.uber.org/zap"
)

// HiddenTool is a specialized tool kept out of the native tool list.
type HiddenTool struct {
	Name        string
	Description string
	Run         func(context.Context, string) (string, error)
}

type callToolInput struct {
	Name      string          `json:"name" jsonschema:"required,description=Hidden tool name from a loaded skill, such as listmaze"`
	Arguments json.RawMessage `json:"arguments,omitempty" jsonschema:"description=JSON object of arguments for that hidden tool"`
}

type callToolOutput struct {
	Name   string          `json:"name"`
	Result json.RawMessage `json:"result"`
}

// HiddenMazeTools wraps maze tools so they can be reached only through call_tool.
func HiddenMazeTools(store *maze.Store) ([]HiddenTool, error) {
	items, err := NewMazeTools(store)
	if err != nil {
		return nil, err
	}
	out := make([]HiddenTool, 0, len(items))
	for _, item := range items {
		inv, ok := item.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("maze tool is not invokable")
		}
		info, err := inv.Info(context.Background())
		if err != nil {
			return nil, err
		}
		run := inv.InvokableRun
		out = append(out, HiddenTool{
			Name:        info.Name,
			Description: info.Desc,
			Run:         func(ctx context.Context, arguments string) (string, error) { return run(ctx, arguments) },
		})
	}
	return out, nil
}

// NewCallTool is the public dispatcher. Native context only sees this schema.
func NewCallTool(hidden []HiddenTool) (tool.InvokableTool, error) {
	index := map[string]HiddenTool{}
	for _, item := range hidden {
		if item.Name == "" || item.Run == nil {
			return nil, fmt.Errorf("hidden tool is missing name or run")
		}
		if _, exists := index[item.Name]; exists {
			return nil, fmt.Errorf("duplicate hidden tool %q", item.Name)
		}
		index[item.Name] = item
	}
	call := func(ctx context.Context, input callToolInput) (callToolOutput, error) {
		item, ok := index[input.Name]
		if !ok {
			err := fmt.Errorf("unknown hidden tool %q; load the matching skill first", input.Name)
			logger.Error("call_tool failed", zap.String("name", input.Name), zap.Error(err))
			return callToolOutput{}, err
		}
		args := "{}"
		if len(input.Arguments) > 0 && string(input.Arguments) != "null" {
			args = string(input.Arguments)
		}
		raw, err := item.Run(ctx, args)
		if err != nil {
			return callToolOutput{}, err
		}
		return callToolOutput{Name: input.Name, Result: json.RawMessage(raw)}, nil
	}
	return utils.InferTool(
		"call_tool",
		"Call a hidden specialized tool by name after load_skill. Arguments is a JSON object. Maze tools: listmaze, detailmaze, runmaze, makemaze.",
		call,
	)
}
