package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"easygo-agent/internal/clientapi"
)

type staticTasks []clientapi.TaskSummary

func (s staticTasks) ListTasks(_ context.Context, user, session string) ([]clientapi.TaskSummary, error) {
	if user != "alice" || session != "session-1" {
		return nil, errors.New("tasks listed for the wrong session")
	}
	return s, nil
}

type staticNotifications []clientapi.RunRecord

func (s staticNotifications) NotificationRuns(context.Context, string, string, int64) ([]clientapi.RunRecord, error) {
	return s, nil
}

func TestBackgroundTaskProgressAndSummaryAreNotDuplicated(t *testing.T) {
	tasks := staticTasks{{ID: "task-id", Version: 1, Status: "running", Progress: "checking", LastSeq: 2}}
	notifications := staticNotifications{{ID: "summary-id", TurnID: 4, ResultText: "verified result", Source: "task_notification"}}
	ui := New(nil).WithTasks(tasks, notifications)
	ui.username, ui.sessionID = "alice", "session-1"
	snapshot := ui.backgroundPoll()().(backgroundSnapshot)
	if snapshot.err != nil {
		t.Fatal(snapshot.err)
	}
	_ = ui.backgroundUpdate(snapshot)
	_ = ui.backgroundUpdate(snapshot)
	text := strings.Join(ui.lines, "\n")
	if strings.Count(text, "verified result") != 1 || strings.Count(text, "checking") != 1 {
		t.Fatalf("duplicated: %s", text)
	}
	if !strings.Contains(text, "task task-id v1 [running]:  checking") {
		t.Fatalf("task line changed: %s", text)
	}
	if ui.notificationAfter != 4 {
		t.Fatal("cursor did not advance")
	}
}
