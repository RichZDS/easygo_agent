package tui

import "fmt"

// Restore renders the audit history. Model context stays owned by the runtime.
// The caller renders its stored turns to transcript lines; lastTurnID is the
// highest turn ID among them and advances the notification cursor.
func (m *Model) Restore(username, id string, lastTurnID int64, lines []string) {
	m.username = username
	m.sessionID = id
	m.lines = append(m.lines, fmt.Sprintf("user: %s · session: %s", username, id))
	if lastTurnID > m.notificationAfter {
		m.notificationAfter = lastTurnID
	}
	m.lines = append(m.lines, lines...)
	if m.queueMode {
		m.loadQueue()
	}
	m.refreshViewport()
}
