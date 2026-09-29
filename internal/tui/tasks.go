package tui

import (
	"context"
	"fmt"
	"time"

	"easygo-agent/internal/clientapi"
	tea "github.com/charmbracelet/bubbletea"
)

type backgroundTick struct{}
type backgroundSnapshot struct {
	tasks         []clientapi.TaskSummary
	notifications []clientapi.RunRecord
	err           error
}

func (m *Model) WithTasks(store clientapi.TaskLister, reader ...clientapi.NotificationReader) *Model {
	m.tasks = store
	if len(reader) > 0 {
		m.notifications = reader[0]
	}
	m.seenNotifications = map[string]bool{}
	return m
}
func backgroundWait() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return backgroundTick{} })
}
func (m *Model) backgroundPoll() tea.Cmd {
	store, reader, user, session, after := m.tasks, m.notifications, m.username, m.sessionID, m.notificationAfter
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		items, err := store.ListTasks(ctx, user, session)
		var runs []clientapi.RunRecord
		if err == nil && reader != nil {
			runs, err = reader.NotificationRuns(ctx, user, session, after)
		}
		return backgroundSnapshot{items, runs, err}
	}
}
func (m *Model) backgroundUpdate(snapshot backgroundSnapshot) tea.Cmd {
	if snapshot.err != nil {
		m.status = "background tasks: " + snapshot.err.Error()
		return backgroundWait()
	}
	for _, t := range snapshot.tasks {
		seq := t.LastSeq
		if m.taskSequences == nil {
			m.taskSequences = map[string]int64{}
		}
		if seq > m.taskSequences[t.ID] {
			m.taskSequences[t.ID] = seq
			m.lines = append(m.lines, fmt.Sprintf("task %.8s v%d [%s]: %s %s", t.ID, t.Version, t.Status, t.Goal, t.Progress))
		}
	}
	for _, r := range snapshot.notifications {
		if !m.seenNotifications[r.ID] && m.queueSubs[r.ID] == nil {
			m.lines = append(m.lines, "assistant (background summary): "+r.ResultText)
			if r.Error != "" {
				m.lines = append(m.lines, "summary error: "+r.Error)
			}
			m.seenNotifications[r.ID] = true
		}
		if r.TurnID > m.notificationAfter {
			m.notificationAfter = r.TurnID
		}
	}
	m.refreshQueue()
	m.refreshViewport()
	return tea.Batch(backgroundWait(), m.waitForQueueEvents())
}
