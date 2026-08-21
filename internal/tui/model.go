// Package tui implements the template's Bubble Tea adapter.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"easygo-agent/internal/gateway"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
)

type runState uint8

const (
	stateIdle runState = iota
	stateRunning
	stateQuitting
)

type gatewayEventMessage struct {
	event gateway.Event
	err   error
}

type streamCloseMessage struct {
	err error
}

// Model is the template's minimal Bubble Tea state machine.
type Model struct {
	runner     gateway.Runner
	logger     *zap.Logger
	input      textarea.Model
	viewport   viewport.Model
	state      runState
	history    []gateway.Message
	lines      []string
	partial    string
	active     gateway.EventStream
	runCancel  context.CancelFunc
	runContext context.Context
	status     string
	width      int
	height     int
}

// New constructs an idle TUI with process-local conversation history.
func New(runner gateway.Runner, systemPrompt string, logger *zap.Logger) *Model {
	if logger == nil {
		logger = zap.NewNop()
	}
	input := textarea.New()
	input.Placeholder = "Ask the agent..."
	input.Prompt = "> "
	input.SetHeight(1)
	input.SetWidth(76)
	input.ShowLineNumbers = false
	input.CharLimit = 16_000
	input.Focus()

	model := &Model{
		runner:   runner,
		logger:   logger,
		input:    input,
		viewport: viewport.New(80, 18),
		state:    stateIdle,
		status:   "idle · Enter send · Ctrl+C quit",
		width:    80,
		height:   24,
	}
	if prompt := strings.TrimSpace(systemPrompt); prompt != "" {
		model.history = append(model.history, gateway.Message{Role: gateway.RoleSystem, Content: prompt})
	}
	model.refreshViewport()
	return model
}

// Init starts the textarea cursor blink command.
func (model *Model) Init() tea.Cmd {
	return textarea.Blink
}

// Update applies terminal input and Gateway events on Bubble Tea's single state thread.
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
	case gatewayEventMessage:
		return model, model.applyGatewayEvent(message)
	case streamCloseMessage:
		if message.err != nil {
			model.logger.Error("close Gateway stream failed", zap.Error(message.err))
			model.lines = append(model.lines, "error: failed to close stream")
			model.refreshViewport()
		}
		return model, nil
	}

	if model.state == stateIdle {
		var command tea.Cmd
		model.input, command = model.input.Update(message)
		return model, command
	}
	return model, nil
}

// handleControlC cancels a running request or quits from idle.
func (model *Model) handleControlC() (tea.Model, tea.Cmd) {
	if model.state == stateRunning {
		model.status = "canceling..."
		if model.runCancel != nil {
			model.runCancel()
		}
		return model, nil
	}
	model.state = stateQuitting
	return model, tea.Quit
}

// submit starts one Gateway run from the current process-local history.
func (model *Model) submit() tea.Cmd {
	content := strings.TrimSpace(model.input.Value())
	if content == "" || model.runner == nil {
		return nil
	}
	model.input.Reset()
	model.lines = append(model.lines, "you: "+content)
	model.history = append(model.history, gateway.Message{Role: gateway.RoleUser, Content: content})
	model.partial = ""
	model.state = stateRunning
	model.status = "running · Ctrl+C cancel"
	model.runContext, model.runCancel = context.WithCancel(context.Background())
	request := gateway.Request{Messages: append([]gateway.Message(nil), model.history...)}
	stream, err := model.runner.Run(model.runContext, request)
	if err != nil {
		model.logger.Error("start TUI run failed", zap.Error(err))
		model.lines = append(model.lines, "error: "+err.Error())
		model.resetRun()
		model.refreshViewport()
		return nil
	}
	model.active = stream
	model.refreshViewport()
	return waitForEvent(stream)
}

// applyGatewayEvent updates transcript state and schedules the next stream receive.
func (model *Model) applyGatewayEvent(message gatewayEventMessage) tea.Cmd {
	if message.err != nil {
		if errors.Is(message.err, io.EOF) {
			return nil
		}
		model.logger.Error("receive TUI Gateway event failed", zap.Error(message.err))
		model.lines = append(model.lines, "error: "+message.err.Error())
		return model.finishRun()
	}

	event := message.event
	switch event.Kind {
	case gateway.EventTextDelta:
		model.partial += event.Text
		model.refreshViewport()
		return waitForEvent(model.active)
	case gateway.EventToolStart:
		model.lines = append(model.lines, fmt.Sprintf("tool: %s started", event.ToolName))
		model.refreshViewport()
		return waitForEvent(model.active)
	case gateway.EventToolEnd:
		if event.Err != nil {
			model.lines = append(model.lines, fmt.Sprintf("tool: %s failed", event.ToolName))
		} else {
			model.lines = append(model.lines, fmt.Sprintf("tool: %s completed", event.ToolName))
		}
		model.refreshViewport()
		return waitForEvent(model.active)
	case gateway.EventCompleted:
		model.lines = append(model.lines, "assistant: "+event.Text)
		model.history = append(model.history, gateway.Message{Role: gateway.RoleAssistant, Content: event.Text})
		model.partial = ""
		return model.finishRun()
	case gateway.EventCanceled:
		if model.partial != "" {
			model.lines = append(model.lines, "assistant (canceled): "+model.partial)
		}
		model.partial = ""
		return model.finishRun()
	case gateway.EventFailed:
		if model.partial != "" {
			model.lines = append(model.lines, "assistant (partial): "+model.partial)
		}
		model.partial = ""
		if event.Err != nil {
			model.logger.Error("Gateway run failed", zap.Error(event.Err))
			model.lines = append(model.lines, "error: "+event.Err.Error())
		} else {
			model.lines = append(model.lines, "error: Gateway run failed")
		}
		return model.finishRun()
	default:
		model.logger.Error("unknown Gateway event", zap.String("event_kind", string(event.Kind)))
		return waitForEvent(model.active)
	}
}

// finishRun returns to idle and closes the completed stream asynchronously.
func (model *Model) finishRun() tea.Cmd {
	stream := model.active
	model.resetRun()
	model.refreshViewport()
	if stream == nil {
		return nil
	}
	return closeStream(stream)
}

// resetRun clears active run state without modifying conversation history.
func (model *Model) resetRun() {
	model.state = stateIdle
	model.status = "idle · Enter send · Ctrl+C quit"
	model.active = nil
	model.runCancel = nil
	model.runContext = nil
}

// waitForEvent creates a Bubble Tea command for one blocking stream receive.
func waitForEvent(stream gateway.EventStream) tea.Cmd {
	// receiveEvent performs one blocking read outside Bubble Tea's Update method.
	return func() tea.Msg {
		event, err := stream.Recv()
		return gatewayEventMessage{event: event, err: err}
	}
}

// closeStream creates a Bubble Tea command that releases one Gateway stream.
func closeStream(stream gateway.EventStream) tea.Cmd {
	// releaseStream closes the stream outside Bubble Tea's Update method.
	return func() tea.Msg {
		return streamCloseMessage{err: stream.Close()}
	}
}

// transcript returns the display transcript including an active partial response.
func (model *Model) transcript() string {
	lines := append([]string(nil), model.lines...)
	if model.partial != "" {
		lines = append(lines, "assistant: "+model.partial)
	}
	return strings.Join(lines, "\n\n")
}

// refreshViewport applies the current transcript and follows its bottom edge.
func (model *Model) refreshViewport() {
	model.viewport.SetContent(model.transcript())
	model.viewport.GotoBottom()
}

// resize updates the transcript and input dimensions for the terminal window.
func (model *Model) resize(width int, height int) {
	model.width = max(width, 20)
	model.height = max(height, 10)
	model.viewport.Width = max(model.width-4, 16)
	model.viewport.Height = max(model.height-9, 3)
	model.input.SetWidth(max(model.width-6, 14))
	model.refreshViewport()
}
