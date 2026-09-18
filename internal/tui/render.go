package tui

import (
	"fmt"
	"strings"

	"easygo-agent/internal/conversation"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	bannerStyle = lipgloss.NewStyle().Bold(true).Align(lipgloss.Center)
	panelStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	statusStyle = lipgloss.NewStyle().Faint(true)
)

// View 渲染 transcript、输入框和一行状态。
func (model *Model) View() string {
	if model.state == stateQuitting {
		return ""
	}
	title := titleStyle.Render("EasyGo Eino Agent Template")
	// Keep the product banner at the top in both the queue-backed and legacy
	// conversation modes. The queue mode adds its status panel immediately
	// below it; the plain mode retains its descriptive title for compatibility.
	banner := bannerStyle.Width(max(model.width-2, 18)).Render("EASY GO")
	queue := ""
	if model.queueMode {
		queue = panelStyle.Width(max(model.width-2, 18)).Render(model.queueView())
	}
	transcript := panelStyle.Width(max(model.width-2, 18)).Render(model.viewport.View())
	input := panelStyle.Width(max(model.width-2, 18)).Render(model.input.View())
	status := statusStyle.Render(model.status)
	if model.queueMode {
		return fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n", banner, queue, transcript, input, status)
	}
	return fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n", banner, title, transcript, input, status)
}

func (model *Model) queueView() string {
	model.ensureQueueSelectionVisible()
	running, queued := 0, 0
	for _, item := range model.queueItems {
		if item.Status == conversation.RunRunning {
			running++
		}
		if item.Status == conversation.RunQueued {
			queued++
		}
	}
	lines := []string{fmt.Sprintf("running: %d · queued: %d", running, queued)}
	maxLines := max(model.height/3, 3)
	for i := model.queueOffset; i < len(model.queueItems); i++ {
		item := model.queueItems[i]
		if len(lines) >= maxLines {
			break
		}
		marker := "  "
		if i == model.selected && model.queueFocus {
			marker = "▶ "
		}
		input := strings.TrimSpace(item.Input)
		if item.Source == "task_notification" {
			input = "后台任务汇总"
		}
		if len([]rune(input)) > 32 {
			input = string([]rune(input)[:32]) + "…"
		}
		shortID := item.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		// Store positions count queued work only (a running item reports zero),
		// while the panel numbers every visible active item. Using the rendered
		// index keeps the sequence unique when a running item precedes queued
		// items.
		position := i + 1
		lines = append(lines, fmt.Sprintf("%s%d. [%s] %-8s %s", marker, position, item.Status, shortID, input))
	}
	return strings.Join(lines, "\n")
}
