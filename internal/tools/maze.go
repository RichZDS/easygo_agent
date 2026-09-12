package tools

import (
	"context"
	"fmt"
	"unicode"

	"easygo-agent/internal/logger"
	"easygo-agent/internal/maze"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"go.uber.org/zap"
)

type makeMazeInput struct {
	Name     string     `json:"name" jsonschema:"required,description=Maze name"`
	Describe string     `json:"describe,omitempty" jsonschema:"description=Short maze description"`
	Grid     [][]string `json:"grid" jsonschema:"required,description=2D array of 墙 路 起 终"`
}

type mazeIDInput struct {
	ID string `json:"id" jsonschema:"required,description=Maze id from listmaze or makemaze"`
}

type runMazeInput struct {
	ID    string `json:"id" jsonschema:"required,description=Maze id from listmaze or makemaze"`
	Moves []int  `json:"moves,omitempty" jsonschema:"description=Move list: 1 up, 2 down, 3 left, 4 right"`
	Path  string `json:"path,omitempty" jsonschema:"description=Optional move string such as 2 4 or 24"`
}

type emptyInput struct{}

// NewMazeTools binds first-class maze tools. Use this only for the bound-tool experiment.
func NewMazeTools(store *maze.Store) ([]tool.BaseTool, error) {
	if store == nil {
		return nil, fmt.Errorf("maze store is required")
	}
	constructors := []func() (tool.InvokableTool, error){
		func() (tool.InvokableTool, error) {
			return utils.InferTool("listmaze", "List saved mazes with id, name, and describe.", func(_ context.Context, _ emptyInput) ([]maze.Summary, error) {
				return store.List()
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("detailmaze", "Show one maze including its 2D grid of 墙 路 起 终.", func(_ context.Context, input mazeIDInput) (maze.Record, error) {
				return store.Detail(input.ID)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("runmaze", "Walk a maze with moves 1上 2下 3左 4右 and report whether 人 reached 终.", func(_ context.Context, input runMazeInput) (maze.RunResult, error) {
				moves, err := parseMoves(input.Moves, input.Path)
				if err != nil {
					return maze.RunResult{}, err
				}
				return store.Run(input.ID, moves)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("makemaze", "Create a maze from a 2D array of 墙 路 起 终.", func(_ context.Context, input makeMazeInput) (maze.Record, error) {
				return store.Make(input.Name, input.Describe, input.Grid)
			})
		},
	}
	out := make([]tool.BaseTool, 0, len(constructors))
	for _, construct := range constructors {
		item, err := construct()
		if err != nil {
			logger.Error("create maze tool failed", zap.Error(err))
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func parseMoves(moves []int, path string) ([]int, error) {
	if len(moves) > 0 {
		return moves, nil
	}
	var out []int
	for _, r := range path {
		if unicode.IsSpace(r) || r == ',' || r == ';' {
			continue
		}
		if r < '1' || r > '4' {
			return nil, fmt.Errorf("invalid move %q; use 1上 2下 3左 4右", string(r))
		}
		out = append(out, int(r-'0'))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("runmaze needs moves or path")
	}
	return out, nil
}
