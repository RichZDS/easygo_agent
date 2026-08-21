package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	panelStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	statusStyle = lipgloss.NewStyle().Faint(true)
)

// View 渲染 transcript、输入框和一行状态。
func (model *Model) View() string {
	if model.state == stateQuitting {
		return ""
	}
	title := titleStyle.Render("EasyGo Eino Agent Template")
	transcript := panelStyle.Width(max(model.width-2, 18)).Render(model.viewport.View())
	input := panelStyle.Width(max(model.width-2, 18)).Render(model.input.View())
	status := statusStyle.Render(model.status)
	return fmt.Sprintf("%s\n%s\n%s\n%s\n", title, transcript, input, status)
}
