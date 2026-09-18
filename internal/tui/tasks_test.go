package tui

import (
	"strings"
	"testing"

	"easygo-agent/internal/conversation"
	"easygo-agent/internal/task"
)

func TestBackgroundTaskProgressAndSummaryAreNotDuplicated(t *testing.T) {
	ui := New(nil).WithTasks(task.NewMemory())
	snapshot := backgroundSnapshot{tasks: []task.Task{{ID: "task-id", Version: 1, Status: task.Running, Progress: "checking", Events: []task.Event{{Seq: 2}}}}, notifications: []conversation.RunRecord{{ID: "summary-id", TurnID: 4, ResultText: "verified result", Source: "task_notification"}}}
	_ = ui.backgroundUpdate(snapshot)
	_ = ui.backgroundUpdate(snapshot)
	text := strings.Join(ui.lines, "\n")
	if strings.Count(text, "verified result") != 1 || strings.Count(text, "checking") != 1 {
		t.Fatalf("duplicated: %s", text)
	}
	if ui.notificationAfter != 4 {
		t.Fatal("cursor did not advance")
	}
}
