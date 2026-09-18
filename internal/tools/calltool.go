package tools

import (
	"context"
	"fmt"

	"easygo-agent/internal/maze"
	"easygo-agent/internal/toolregistry"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// HiddenTool is a specialized tool kept out of the native tool list.
type HiddenTool struct {
	Schema      *schema.ToolInfo
	Name        string
	Description string
	Run         func(context.Context, string) (string, error)
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
			Schema:      info,
			Name:        info.Name,
			Description: info.Desc,
			Run:         func(ctx context.Context, arguments string) (string, error) { return run(ctx, arguments) },
		})
	}
	return out, nil
}

// NewCallTool is the public dispatcher. Native context only sees this schema.
func NewCallTool(hidden []HiddenTool) (tool.InvokableTool, error) {
	registry := toolregistry.New()
	for _, item := range hidden {
		if item.Name == "" || item.Run == nil {
			return nil, fmt.Errorf("hidden tool is missing name or run")
		}
		if err := registry.Register(context.Background(), hiddenAdapter{item}, "legacy", toolregistry.Unsafe, true); err != nil {
			return nil, err
		}
	}
	return registry.Dispatcher()
}

type hiddenAdapter struct{ HiddenTool }

func (h hiddenAdapter) Info(context.Context) (*schema.ToolInfo, error) {
	if h.Schema != nil {
		return h.Schema, nil
	}
	return &schema.ToolInfo{Name: h.HiddenTool.Name, Desc: h.Description}, nil
}
func (h hiddenAdapter) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	return h.HiddenTool.Run(ctx, args)
}
