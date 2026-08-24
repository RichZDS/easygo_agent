// Package tui 实现终端交互与展示，不负责 Agent 框架的运行细节。
package tui

import (
	"fmt"
	"strings"

	agentruntime "easygo-agent/internal/agent/runtime"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type runState uint8

const (
	stateIdle runState = iota
	stateRunning
	stateQuitting
)

type runEventMessage struct {
	event agentruntime.Event
}

// Model 是只处理终端输入、展示状态和运行语义事件的 Bubble Tea 状态机。
type Model struct {
	conversation agentruntime.Conversation
	input        textarea.Model
	viewport     viewport.Model
	state        runState
	lines        []string
	partial      string
	activeRun    agentruntime.Run
	status       string
	width        int
	height       int
}

// New 构造空闲 TUI。对话历史与 Agent 生命周期由 conversation 管理。
func New(conversation agentruntime.Conversation) *Model {
	input := textarea.New()
	input.Placeholder = "Ask the agent..."
	input.Prompt = "> "
	input.SetHeight(1)
	input.SetWidth(76)
	input.ShowLineNumbers = false
	input.CharLimit = 16_000
	input.Focus()

	model := &Model{
		conversation: conversation,
		input:        input,
		viewport:     viewport.New(80, 18),
		state:        stateIdle,
		status:       "idle · Enter send · Ctrl+C quit",
		width:        80,
		height:       24,
	}
	model.refreshViewport()
	return model
}

// Init 启动 textarea 光标闪烁命令。
func (model *Model) Init() tea.Cmd {
	return textarea.Blink
}

// Update 在 Bubble Tea 单线程中处理终端输入和 Agent 语义事件。
func (model *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		model.resize(message.Width, message.Height)
		return model, nil
	case tea.KeyMsg:
		if message.Type == tea.KeyCtrlC {
			return model.handleControlC()
		}
		if message.Type == tea.KeyEnter {
			if model.state == stateIdle {
				return model, model.submit()
			}
			return model, nil
		}
	case runEventMessage:
		return model, model.applyRunEvent(message.event)
	}

	if model.state == stateIdle {
		var command tea.Cmd
		model.input, command = model.input.Update(message)
		return model, command
	}
	return model, nil
}

// handleControlC 在运行中取消请求，或在空闲时退出。
func (model *Model) handleControlC() (tea.Model, tea.Cmd) {
	if model.state == stateRunning {
		model.status = "canceling..."
		if model.activeRun != nil {
			model.activeRun.Cancel()
		}
		return model, nil
	}
	model.state = stateQuitting
	return model, tea.Quit
}

// submit 把输入交给对话模块，并开始等待语义事件。
func (model *Model) submit() tea.Cmd {
	content := strings.TrimSpace(model.input.Value())
	if content == "" || model.conversation == nil {
		return nil
	}

	model.input.Reset()
	model.lines = append(model.lines, "you: "+content)
	model.partial = ""
	run, err := model.conversation.Start(content)
	if err != nil {
		model.lines = append(model.lines, "error: "+err.Error())
		model.refreshViewport()
		return nil
	}

	model.activeRun = run
	model.state = stateRunning
	model.status = "running · Ctrl+C cancel"
	model.refreshViewport()
	return waitForRunEvent(run)
}

// applyRunEvent 只把运行模块的语义事件投影到 transcript。
func (model *Model) applyRunEvent(event agentruntime.Event) tea.Cmd {
	switch event.Kind {
	case agentruntime.EventTextDelta:
		model.partial += event.Text
		model.refreshViewport()
		return model.nextRunEvent()
	case agentruntime.EventToolStarted:
		model.lines = append(model.lines, fmt.Sprintf("tool: %s started", event.Tool))
		model.refreshViewport()
		return model.nextRunEvent()
	case agentruntime.EventToolFinished:
		model.lines = append(model.lines, fmt.Sprintf("tool: %s completed", event.Tool))
		model.refreshViewport()
		return model.nextRunEvent()
	case agentruntime.EventCompleted:
		model.partial = event.Text
		return model.finishCompleted()
	case agentruntime.EventCanceled:
		model.partial = event.Text
		return model.finishCanceled()
	case agentruntime.EventFailed:
		model.partial = event.Text
		return model.finishFailed(event.Err)
	default:
		return model.finishFailed(fmt.Errorf("unknown agent event %q", event.Kind))
	}
}

func (model *Model) nextRunEvent() tea.Cmd {
	if model.activeRun == nil {
		return nil
	}
	return waitForRunEvent(model.activeRun)
}

// finishCompleted 把完整助手文本固定到 transcript 后回到空闲。
func (model *Model) finishCompleted() tea.Cmd {
	if model.partial != "" {
		model.lines = append(model.lines, "assistant: "+model.partial)
		model.partial = ""
	}
	return model.finishRun()
}

// finishCanceled 保留已产生的部分输出，但不改变其运行语义。
func (model *Model) finishCanceled() tea.Cmd {
	if model.partial != "" {
		model.lines = append(model.lines, "assistant (canceled): "+model.partial)
		model.partial = ""
	}
	return model.finishRun()
}

// finishFailed 展示部分输出和运行错误。
func (model *Model) finishFailed(err error) tea.Cmd {
	if model.partial != "" {
		model.lines = append(model.lines, "assistant (partial): "+model.partial)
		model.partial = ""
	}
	if err != nil {
		model.lines = append(model.lines, "error: "+err.Error())
	} else {
		model.lines = append(model.lines, "error: agent run failed")
	}
	return model.finishRun()
}

// finishRun 清理 TUI 对当前运行的引用；运行资源由 agentruntime 终态负责释放。
func (model *Model) finishRun() tea.Cmd {
	model.state = stateIdle
	model.status = "idle · Enter send · Ctrl+C quit"
	model.activeRun = nil
	model.refreshViewport()
	return nil
}

// waitForRunEvent 在 Update 方法外阻塞读取下一条运行事件。
func waitForRunEvent(run agentruntime.Run) tea.Cmd {
	return func() tea.Msg {
		return runEventMessage{event: run.Next()}
	}
}

// transcript 返回展示用 transcript，包含进行中的部分响应。
func (model *Model) transcript() string {
	lines := append([]string(nil), model.lines...)
	if model.partial != "" {
		lines = append(lines, "assistant: "+model.partial)
	}
	return strings.Join(lines, "\n\n")
}

// refreshViewport 应用当前 transcript 并跟随底部。
func (model *Model) refreshViewport() {
	model.viewport.SetContent(model.transcript())
	model.viewport.GotoBottom()
}

// resize 按终端窗口更新 transcript 与输入框尺寸。
func (model *Model) resize(width int, height int) {
	model.width = max(width, 20)
	model.height = max(height, 10)
	model.viewport.Width = max(model.width-4, 16)
	model.viewport.Height = max(model.height-9, 3)
	model.input.SetWidth(max(model.width-6, 14))
	model.refreshViewport()
}
