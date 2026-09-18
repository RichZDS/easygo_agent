package tui

import (
	"context"
	"fmt"
	"time"

	"easygo-agent/internal/conversation"
	"easygo-agent/internal/task"
	tea "github.com/charmbracelet/bubbletea"
)

type backgroundTick struct{}
type backgroundSnapshot struct {
	tasks         []task.Task
	notifications []conversation.RunRecord
	err           error
}

func (m *Model) WithTasks(store task.Store, reader ...conversation.NotificationReader) *Model {
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
		items, err := store.List(ctx, task.Owner{User: user, Session: session})
		var runs []conversation.RunRecord
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
		seq := int64(0)
		if len(t.Events) > 0 {
			seq = t.Events[len(t.Events)-1].Seq
		}
		if m.taskSequences == nil {
			m.taskSequences = map[string]int64{}
		}
		if seq > m.taskSequences[t.ID] {
			m.taskSequences[t.ID] = seq
			m.lines = append(m.lines, fmt.Sprintf("task %.8s v%d [%s]: %s %s", t.ID, t.Version, t.Status, t.Brief.Goal, t.Progress))
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
