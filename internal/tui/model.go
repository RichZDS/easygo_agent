// Package tui 实现模板的 Bubble Tea 适配器。
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"easygo-agent/internal/logger"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

type runState uint8

const (
	stateIdle runState = iota
	stateRunning
	stateQuitting
)

type agentEventMessage struct {
	event *adk.AgentEvent
	ended bool
}

type messageChunkMessage struct {
	message *schema.Message
	err     error
	eof     bool
}

// Model 是模板的最小 Bubble Tea 状态机。
type Model struct {
	agent      adk.Agent
	input      textarea.Model
	viewport   viewport.Model
	state      runState
	history    []*schema.Message
	lines      []string
	partial    string
	iter       *adk.AsyncIterator[*adk.AgentEvent]
	msgStream  *schema.StreamReader[*schema.Message]
	runCancel  context.CancelFunc
	runContext context.Context
	status     string
	width      int
	height     int
}

// New 构造带有进程内对话历史的空闲 TUI。
func New(agent adk.Agent, systemPrompt string) *Model {
	input := textarea.New()
	input.Placeholder = "Ask the agent..."
	input.Prompt = "> "
	input.SetHeight(1)
	input.SetWidth(76)
	input.ShowLineNumbers = false
	input.CharLimit = 16_000
	input.Focus()

	model := &Model{
		agent:    agent,
		input:    input,
		viewport: viewport.New(80, 18),
		state:    stateIdle,
		status:   "idle · Enter send · Ctrl+C quit",
		width:    80,
		height:   24,
	}
	if prompt := strings.TrimSpace(systemPrompt); prompt != "" {
		model.history = append(model.history, schema.SystemMessage(prompt))
	}
	model.refreshViewport()
	return model
}

// Init 启动 textarea 光标闪烁命令。
func (model *Model) Init() tea.Cmd {
	return textarea.Blink
}

// Update 在 Bubble Tea 单线程中处理终端输入和 Eino Agent 事件。
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
	case agentEventMessage:
		return model, model.applyAgentEvent(message)
	case messageChunkMessage:
		return model, model.applyMessageChunk(message)
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
		if model.runCancel != nil {
			model.runCancel()
		}
		return model, nil
	}
	model.state = stateQuitting
	return model, tea.Quit
}

// submit 基于当前进程内历史直接启动一次 Eino Agent 运行。
func (model *Model) submit() tea.Cmd {
	content := strings.TrimSpace(model.input.Value())
	if content == "" || model.agent == nil {
		return nil
	}
	model.input.Reset()
	model.lines = append(model.lines, "you: "+content)
	model.history = append(model.history, schema.UserMessage(content))
	model.partial = ""
	model.state = stateRunning
	model.status = "running · Ctrl+C cancel"
	model.runContext, model.runCancel = context.WithCancel(context.Background())
	model.iter = model.agent.Run(model.runContext, &adk.AgentInput{
		Messages:        slices.Clone(model.history),
		EnableStreaming: true,
	})
	model.refreshViewport()
	return model.nextCmd()
}

// nextCmd 优先消费当前消息流，否则读取下一条 AgentEvent。
func (model *Model) nextCmd() tea.Cmd {
	if model.msgStream != nil {
		return waitForChunk(model.msgStream)
	}
	if model.iter != nil {
		return waitForAgentEvent(model.iter)
	}
	return nil
}

// applyAgentEvent 把一条原生 ADK 事件投影到 transcript。
func (model *Model) applyAgentEvent(message agentEventMessage) tea.Cmd {
	if model.canceled() {
		return model.finishCanceled()
	}
	if message.ended {
		return model.finishCompleted()
	}
	event := message.event
	if event == nil {
		return model.nextCmd()
	}
	if event.Err != nil {
		if model.canceled() {
			return model.finishCanceled()
		}
		return model.finishFailed(event.Err)
	}
	if event.Output == nil || event.Output.MessageOutput == nil {
		return model.nextCmd()
	}
	return model.applyMessageOutput(event.Output.MessageOutput)
}

// applyMessageOutput 按 Eino 原生 Role 处理助手文本或 Tool 结果。
func (model *Model) applyMessageOutput(output *adk.MessageVariant) tea.Cmd {
	if output.IsStreaming && output.MessageStream != nil {
		output.MessageStream.SetAutomaticClose()
		if output.Role == schema.Assistant {
			model.msgStream = output.MessageStream
			return model.nextCmd()
		}
		if output.Role == schema.Tool {
			model.lines = append(model.lines, fmt.Sprintf("tool: %s completed", output.ToolName))
			model.refreshViewport()
		}
		output.MessageStream.Close()
		return model.nextCmd()
	}
	if output.Role == schema.Tool {
		model.lines = append(model.lines, fmt.Sprintf("tool: %s completed", output.ToolName))
		model.refreshViewport()
		return model.nextCmd()
	}
	if output.Role == schema.Assistant && output.Message != nil {
		model.applyAssistantMessage(output.Message)
		model.refreshViewport()
	}
	return model.nextCmd()
}

// applyMessageChunk 消费助手消息流的一个分片。
func (model *Model) applyMessageChunk(message messageChunkMessage) tea.Cmd {
	if model.canceled() {
		return model.finishCanceled()
	}
	if message.eof {
		model.closeMessageStream()
		return model.nextCmd()
	}
	if message.err != nil {
		if model.canceled() {
			return model.finishCanceled()
		}
		wrappedErr := fmt.Errorf("receive Eino message stream: %w", message.err)
		logger.Error("receive TUI agent stream failed", zap.Error(wrappedErr))
		return model.finishFailed(wrappedErr)
	}
	if message.message != nil {
		model.applyAssistantMessage(message.message)
		model.refreshViewport()
	}
	return model.nextCmd()
}

// applyAssistantMessage 累积助手文本，并把 ToolCalls 显示为 Tool 开始。
func (model *Model) applyAssistantMessage(message *schema.Message) {
	if message.Content != "" {
		model.partial += message.Content
	}
	for _, call := range message.ToolCalls {
		name := call.Function.Name
		if name == "" {
			name = call.ID
		}
		model.lines = append(model.lines, fmt.Sprintf("tool: %s started", name))
	}
}

// finishCompleted 把累积的助手文本写入历史后回到空闲。
func (model *Model) finishCompleted() tea.Cmd {
	if model.partial != "" {
		model.lines = append(model.lines, "assistant: "+model.partial)
		model.history = append(model.history, schema.AssistantMessage(model.partial, nil))
		model.partial = ""
	}
	return model.finishRun()
}

// finishCanceled 只展示部分输出，不写入下一轮历史。
func (model *Model) finishCanceled() tea.Cmd {
	if model.partial != "" {
		model.lines = append(model.lines, "assistant (canceled): "+model.partial)
		model.partial = ""
	}
	return model.finishRun()
}

// finishFailed 只展示部分输出和错误，不写入下一轮历史。
func (model *Model) finishFailed(err error) tea.Cmd {
	if model.partial != "" {
		model.lines = append(model.lines, "assistant (partial): "+model.partial)
		model.partial = ""
	}
	if err != nil {
		logger.Error("agent run failed", zap.Error(err))
		model.lines = append(model.lines, "error: "+err.Error())
	} else {
		model.lines = append(model.lines, "error: agent run failed")
	}
	return model.finishRun()
}

// finishRun 回到空闲状态并释放本次运行资源。
func (model *Model) finishRun() tea.Cmd {
	model.closeMessageStream()
	model.resetRun()
	model.refreshViewport()
	return nil
}

// resetRun 清除进行中的运行状态，但不修改对话历史。
func (model *Model) resetRun() {
	model.state = stateIdle
	model.status = "idle · Enter send · Ctrl+C quit"
	model.iter = nil
	model.closeMessageStream()
	if model.runCancel != nil {
		model.runCancel()
	}
	model.runCancel = nil
	model.runContext = nil
}

// canceled 报告调用方是否已取消本次运行。
func (model *Model) canceled() bool {
	return model.runContext != nil && model.runContext.Err() != nil
}

// closeMessageStream 关闭尚未读完的助手消息流。
func (model *Model) closeMessageStream() {
	if model.msgStream == nil {
		return
	}
	model.msgStream.Close()
	model.msgStream = nil
}

// waitForAgentEvent 在 Update 方法外阻塞读取下一条 ADK 事件。
func waitForAgentEvent(iter *adk.AsyncIterator[*adk.AgentEvent]) tea.Cmd {
	return func() tea.Msg {
		event, ok := iter.Next()
		if !ok {
			return agentEventMessage{ended: true}
		}
		return agentEventMessage{event: event}
	}
}

// waitForChunk 在 Update 方法外阻塞读取一个助手流分片。
func waitForChunk(stream *schema.StreamReader[*schema.Message]) tea.Cmd {
	return func() tea.Msg {
		message, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return messageChunkMessage{eof: true}
			}
			return messageChunkMessage{err: err}
		}
		return messageChunkMessage{message: message}
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
