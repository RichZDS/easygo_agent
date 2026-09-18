package tui

import (
	"easygo-agent/internal/conversation"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"strings"
)

// Restore renders the audit history. Model context stays owned by the runtime.
func (m *Model) Restore(username, id string, turns []conversation.Turn) {
	m.username = username
	m.sessionID = id
	m.lines = append(m.lines, fmt.Sprintf("user: %s · session: %s", username, id))
	for _, turn := range turns {
		if turn.ID > m.notificationAfter {
			m.notificationAfter = turn.ID
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
					m.lines = append(m.lines, "tool: "+b.FunctionToolCall.Name+" called")
				}
				if b.FunctionToolResult != nil {
					m.lines = append(m.lines, "tool: "+b.FunctionToolResult.Name+" returned")
				}
			}
			role := "assistant"
			if message.Role == schema.AgenticRoleTypeUser {
				role = "you"
			}
			if text.Len() > 0 {
				m.lines = append(m.lines, role+": "+text.String())
			}
		}
		if turn.Status != "completed" {
			m.lines = append(m.lines, "run: "+turn.Status)
		}
	}
	if m.queueMode {
		m.loadQueue()
	}
	m.refreshViewport()
}
