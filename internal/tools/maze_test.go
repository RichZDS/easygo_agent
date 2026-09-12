package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"easygo-agent/internal/maze"

	"github.com/cloudwego/eino/components/tool"
)

func TestMazeToolsCreateListDetailAndRun(t *testing.T) {
	store := maze.NewStore(t.TempDir())
	items, err := NewMazeTools(store)
	if err != nil {
		t.Fatal(err)
	}
	byName := invokableByName(t, items)
	makeRaw, err := byName["makemaze"].InvokableRun(context.Background(), `{
		"name":"小径","describe":"两步到终点",
		"grid":[["墙","墙","墙","墙"],["墙","起","路","墙"],["墙","路","终","墙"],["墙","墙","墙","墙"]]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	var created maze.Record
	if err = json.Unmarshal([]byte(makeRaw), &created); err != nil || created.ID == "" {
		t.Fatalf("makemaze=%s err=%v", makeRaw, err)
	}
	listRaw, err := byName["listmaze"].InvokableRun(context.Background(), `{}`)
	if err != nil || !strings.Contains(listRaw, created.ID) {
		t.Fatalf("listmaze=%s err=%v", listRaw, err)
	}
	detailRaw, err := byName["detailmaze"].InvokableRun(context.Background(), `{"id":"`+created.ID+`"}`)
	if err != nil || !strings.Contains(detailRaw, "起") {
		t.Fatalf("detailmaze=%s err=%v", detailRaw, err)
	}
	runRaw, err := byName["runmaze"].InvokableRun(context.Background(), `{"id":"`+created.ID+`","moves":[2,4]}`)
	if err != nil || !strings.Contains(runRaw, `"reached":true`) {
		t.Fatalf("runmaze=%s err=%v", runRaw, err)
	}
}

func TestCallToolDispatchesHiddenMazeTools(t *testing.T) {
	store := maze.NewStore(t.TempDir())
	hidden, err := HiddenMazeTools(store)
	if err != nil {
		t.Fatal(err)
	}
	item, err := NewCallTool(hidden)
	if err != nil {
		t.Fatal(err)
	}
	invokable := item.(tool.InvokableTool)
	raw, err := invokable.InvokableRun(context.Background(), `{
		"name":"makemaze",
		"arguments":{"name":"小径","describe":"两步","grid":[["墙","墙","墙"],["墙","起","终"],["墙","墙","墙"]]}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "小径") {
		t.Fatalf("call_tool makemaze=%s", raw)
	}
}

func TestHeuristicAllToolsHidesMazeNames(t *testing.T) {
	store := maze.NewStore(t.TempDir())
	items, err := NewAgentTool().WithHiddenMaze(store).AllTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(t, items)
	if !names["call_tool"] || names["listmaze"] || names["makemaze"] {
		t.Fatalf("heuristic tools=%v", names)
	}
}

func TestBoundAllToolsExposesMazeNames(t *testing.T) {
	store := maze.NewStore(t.TempDir())
	items, err := NewAgentTool().WithBoundMaze(store).AllTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(t, items)
	if !names["listmaze"] || !names["detailmaze"] || !names["runmaze"] || !names["makemaze"] || names["call_tool"] {
		t.Fatalf("bound tools=%v", names)
	}
}

func invokableByName(t *testing.T, items []tool.BaseTool) map[string]tool.InvokableTool {
	t.Helper()
	out := map[string]tool.InvokableTool{}
	for _, item := range items {
		info, err := item.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		inv, ok := item.(tool.InvokableTool)
		if !ok {
			t.Fatalf("%s is not invokable", info.Name)
		}
		out[info.Name] = inv
	}
	return out
}

func toolNames(t *testing.T, items []tool.BaseTool) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, item := range items {
		info, err := item.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out[info.Name] = true
	}
	return out
}
