package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"easygo-agent/internal/clientapi"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/task"

	"github.com/cloudwego/eino/schema"
)

// Expected lines were captured from tui.Restore before it moved here.
func TestHistoryLinesKeepTerminalRendering(t *testing.T) {
	message := func(role schema.AgenticRoleType, blocks ...*schema.ContentBlock) *schema.AgenticMessage {
		return &schema.AgenticMessage{Role: role, ContentBlocks: blocks}
	}
	userText := func(s string) *schema.ContentBlock {
		return &schema.ContentBlock{UserInputText: &schema.UserInputText{Text: s}}
	}
	genText := func(s string) *schema.ContentBlock {
		return &schema.ContentBlock{AssistantGenText: &schema.AssistantGenText{Text: s}}
	}
	history := []conversation.Turn{
		{ID: 3, Status: "completed", Messages: []*schema.AgenticMessage{
			message(schema.AgenticRoleTypeSystem, genText("system prompt")),
			nil,
			message(schema.AgenticRoleTypeUser, userText("hello")),
			message(schema.AgenticRoleTypeAssistant, genText("let me "), &schema.ContentBlock{FunctionToolCall: &schema.FunctionToolCall{Name: "calc"}}, nil, genText("compute")),
			message(schema.AgenticRoleTypeUser, &schema.ContentBlock{FunctionToolResult: &schema.FunctionToolResult{Name: "calc"}}),
			message(schema.AgenticRoleTypeAssistant, genText("42")),
		}},
		{ID: 9, Status: "failed", Messages: []*schema.AgenticMessage{message(schema.AgenticRoleTypeUser, userText("boom"))}},
		{ID: 5, Status: "canceled", Messages: []*schema.AgenticMessage{message(schema.AgenticRoleTypeAssistant, genText("partial"))}},
		{ID: 6, Status: "completed"},
	}
	lastTurnID, lines := historyLines(history)
	want := []string{"you: hello", "tool: calc called", "assistant: let me compute", "tool: calc returned", "assistant: 42", "you: boom", "run: failed", "assistant: partial", "run: canceled"}
	if lastTurnID != 9 || !slices.Equal(lines, want) {
		t.Fatalf("history rendering changed: %d %q", lastTurnID, lines)
	}
}

type listedTasks struct {
	task.Store
	owner task.Owner
	items []task.Task
	err   error
}

func (l *listedTasks) List(_ context.Context, owner task.Owner) ([]task.Task, error) {
	l.owner = owner
	return l.items, l.err
}

func TestTaskListerSummarizesSessionTasks(t *testing.T) {
	store := &listedTasks{items: []task.Task{
		{ID: "0123456789abcdef", Version: 2, Status: task.Running, Brief: task.Brief{Goal: "check docs"}, Progress: "reading", Events: []task.Event{{Seq: 1}, {Seq: 3}}},
		{ID: "short", Version: 1, Status: task.Queued, Brief: task.Brief{Goal: "no events"}},
	}}
	got, err := taskLister{store}.ListTasks(context.Background(), "alice", "sess-1")
	want := []clientapi.TaskSummary{
		{ID: "0123456789abcdef", Version: 2, Status: "running", Goal: "check docs", Progress: "reading", LastSeq: 3},
		{ID: "short", Version: 1, Status: "queued", Goal: "no events"},
	}
	if err != nil || !slices.Equal(got, want) || store.owner != (task.Owner{User: "alice", Session: "sess-1"}) {
		t.Fatalf("summaries %+v owner %+v err %v", got, store.owner, err)
	}
	store.err = errors.New("store unavailable")
	if got, err = (taskLister{store}).ListTasks(context.Background(), "alice", "sess-1"); err == nil || got != nil {
		t.Fatalf("store error was not returned: %+v %v", got, err)
	}
}
