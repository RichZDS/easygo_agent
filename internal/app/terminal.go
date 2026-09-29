package app

import (
	"context"
	"strings"

	"easygo-agent/internal/clientapi"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/task"

	"github.com/cloudwego/eino/schema"
)

// taskLister shows the background tasks of the local app in the terminal UI.
type taskLister struct{ store task.Store }

func (l taskLister) ListTasks(ctx context.Context, user, session string) ([]clientapi.TaskSummary, error) {
	items, err := l.store.List(ctx, task.Owner{User: user, Session: session})
	if err != nil {
		return nil, err
	}
	summaries := make([]clientapi.TaskSummary, 0, len(items))
	for _, t := range items {
		seq := int64(0)
		if len(t.Events) > 0 {
			seq = t.Events[len(t.Events)-1].Seq
		}
		summaries = append(summaries, clientapi.TaskSummary{ID: t.ID, Version: t.Version, Status: string(t.Status), Goal: t.Brief.Goal, Progress: t.Progress, LastSeq: seq})
	}
	return summaries, nil
}

// historyLines renders stored audit turns for the terminal UI. It returns the
// highest turn ID, which the UI keeps as its notification cursor.
func historyLines(turns []conversation.Turn) (int64, []string) {
	var lastTurnID int64
	var lines []string
	for _, turn := range turns {
		if turn.ID > lastTurnID {
			lastTurnID = turn.ID
		}
		for _, message := range turn.Messages {
			if message == nil || message.Role == schema.AgenticRoleTypeSystem {
				continue
			}
			var text strings.Builder
			for _, b := range message.ContentBlocks {
				if b == nil {
					continue
				}
				if b.UserInputText != nil {
					text.WriteString(b.UserInputText.Text)
				}
				if b.AssistantGenText != nil {
					text.WriteString(b.AssistantGenText.Text)
				}
				if b.FunctionToolCall != nil {
					lines = append(lines, "tool: "+b.FunctionToolCall.Name+" called")
				}
				if b.FunctionToolResult != nil {
					lines = append(lines, "tool: "+b.FunctionToolResult.Name+" returned")
				}
			}
			role := "assistant"
			if message.Role == schema.AgenticRoleTypeUser {
				role = "you"
			}
			if text.Len() > 0 {
				lines = append(lines, role+": "+text.String())
			}
		}
		if turn.Status != "completed" {
			lines = append(lines, "run: "+turn.Status)
		}
	}
	return lastTurnID, lines
}
