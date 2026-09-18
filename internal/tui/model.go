// Package tui 实现终端交互与展示，不负责 Agent 框架的运行细节。
package tui

import (
	"context"
	"fmt"
	"strings"

	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/conversation"
	"easygo-agent/internal/task"

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

type queueEventMessage struct {
	runID string
	event agentruntime.Event
	ok    bool
}

// Model 是只处理终端输入、展示状态和运行语义事件的 Bubble Tea 状态机。
type Model struct {
	tasks             task.Store
	notifications     conversation.NotificationReader
	taskSequences     map[string]int64
	seenNotifications map[string]bool
	notificationAfter int64
	queue             agentruntime.QueueManager
	username          string
	sessionID         string
	queueMode         bool
	queueItems        []conversation.RunRecord
	queueFocus        bool
	selected          int
	queueOffset       int
	activeRunID       string
	queueSubs         map[string]agentruntime.Subscription
	queueWaiting      map[string]bool
	partials          map[string]string
	reasonings        map[string]string
	input             textarea.Model
	viewport          viewport.Model
	state             runState
	lines             []string
	partial           string
	reasoning         string
	status            string
	width             int
	height            int
}

// New 构造 queue-backed TUI。
func New(source agentruntime.QueueManager, identity ...string) *Model {
	input := textarea.New()
	input.Placeholder = "Ask the agent..."
	input.Prompt = "> "
	input.SetHeight(1)
	input.SetWidth(76)
	input.ShowLineNumbers = false
	input.CharLimit = 16_000
	input.Focus()

	model := &Model{
		input:        input,
		viewport:     viewport.New(80, 18),
		state:        stateIdle,
		status:       "idle · Enter send · Ctrl+C quit",
		width:        80,
		height:       24,
		queueSubs:    make(map[string]agentruntime.Subscription),
		queueWaiting: make(map[string]bool),
		partials:     make(map[string]string),
		reasonings:   make(map[string]string),
	}
	if source != nil {
		model.queue = source
		model.queueMode = true
		if len(identity) > 0 {
			model.username = identity[0]
		}
		if len(identity) > 1 {
			model.sessionID = identity[1]
		}
	}
	if model.queueMode && model.username != "" && model.sessionID != "" {
		model.loadQueue()
	}
	model.refreshViewport()
	return model
}

// NewQueue constructs the queue-backed TUI and restores active durable runs.
func NewQueue(manager agentruntime.QueueManager, username, sessionID string) *Model {
	model := New(manager)
	model.username = username
	model.sessionID = sessionID
	model.loadQueue()
	return model
}

// Init 启动 textarea 光标闪烁命令。
func (model *Model) Init() tea.Cmd {
	commands := []tea.Cmd{textarea.Blink}
	if model.tasks != nil {
		commands = append(commands, backgroundWait())
	}
	if model.queueMode {
		if command := model.waitForQueueEvents(); command != nil {
			commands = append(commands, command)
		}
	}
	return tea.Batch(commands...)
}

// Close is called after the terminal program exits, including external errors.
// Runtime waits for persistence and lease release without consuming UI events.
func (model *Model) Close() error {
	for id, subscription := range model.queueSubs {
		subscription.Close()
		delete(model.queueSubs, id)
		delete(model.queueWaiting, id)
	}
	return nil
}

// Update 在 Bubble Tea 单线程中处理终端输入和 Agent 语义事件。
func (model *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case backgroundTick:
		return model, model.backgroundPoll()
	case backgroundSnapshot:
		return model, model.backgroundUpdate(message)
	case tea.WindowSizeMsg:
		model.resize(message.Width, message.Height)
		return model, nil
	case tea.KeyMsg:
		if message.Type == tea.KeyTab && model.queueMode {
			model.queueFocus = !model.queueFocus
			model.status = model.focusStatus()
			if model.queueFocus {
				model.input.Blur()
				return model, nil
			}
			return model, model.input.Focus()
		}
		if message.Type == tea.KeyCtrlC {
			return model.handleControlC()
		}
		if message.Type == tea.KeyEnter {
			if model.queueFocus {
				return model, nil
			}
			return model, model.submit()
		}
		if model.queueMode && model.queueFocus {
			switch message.Type {
			case tea.KeyUp:
				model.selectQueue(-1)
			case tea.KeyDown:
				model.selectQueue(1)
			case tea.KeyDelete, tea.KeyBackspace:
				return model, model.cancelSelected()
			}
			return model, nil
		}
	case queueEventMessage:
		return model, model.applyQueueEvent(message)
	}

	if model.queueMode || model.state == stateIdle {
		var command tea.Cmd
		model.input, command = model.input.Update(message)
		return model, command
	}
	return model, nil
}

// handleControlC 在运行中取消请求，或在空闲时退出。
func (model *Model) handleControlC() (tea.Model, tea.Cmd) {
	if model.queueMode {
		// Ctrl+C always targets the currently running request. Queued requests
		// are canceled explicitly with Delete while the queue has focus.
		id := model.activeRunID
		if id != "" {
			foundRunning := false
			for _, item := range model.queueItems {
				if item.ID == id && item.Status == conversation.RunRunning {
					foundRunning = true
					break
				}
			}
			if !foundRunning {
				id = ""
			}
		}
		if id == "" {
			for _, item := range model.queueItems {
				if item.Status == conversation.RunRunning {
					id = item.ID
					break
				}
			}
		}
		if id != "" {
			model.status = "canceling..."
			return model, func() tea.Msg {
				_, _ = model.queue.Cancel(context.Background(), model.username, model.sessionID, id)
				return nil
			}
		}
		if len(model.queueItems) > 0 {
			return model, nil
		}
		model.state = stateQuitting
		return model, tea.Quit
	}
	model.state = stateQuitting
	return model, tea.Quit
}

// submit 把输入交给对话模块，并开始等待语义事件。
func (model *Model) submit() tea.Cmd {
	content := strings.TrimSpace(model.input.Value())
	if content == "" || model.queue == nil {
		return nil
	}

	model.input.Reset()
	model.lines = append(model.lines, "you: "+content)
	model.partial = ""
	model.reasoning = ""
	return model.submitQueue(content)
}

func (model *Model) submitQueue(content string) tea.Cmd {
	record, _, err := model.queue.Submit(context.Background(), model.username, model.sessionID, content, "")
	if err != nil {
		model.input.SetValue(content)
		model.lines = append(model.lines, "error: "+err.Error())
		model.refreshViewport()
		return nil
	}
	if record.Status == conversation.RunRunning {
		model.activeRunID = record.ID
	}
	model.state = stateRunning
	model.status = model.focusStatus()
	model.refreshQueue()
	model.subscribeQueueRun(record.ID)
	model.refreshViewport()
	return model.waitForQueueEvents()
}

func (model *Model) subscribeQueueRun(runID string) {
	if _, exists := model.queueSubs[runID]; exists {
		return
	}
	subscription, err := model.queue.Subscribe(context.Background(), model.username, model.sessionID, runID)
	if err != nil {
		model.lines = append(model.lines, "error: "+err.Error())
		return
	}
	model.queueSubs[runID] = subscription
}

func waitForQueueEvent(model *Model, runID string) tea.Cmd {
	subscription := model.queueSubs[runID]
	return func() tea.Msg {
		if subscription == nil {
			return queueEventMessage{runID: runID, ok: false}
		}
		event, ok := <-subscription.Events()
		return queueEventMessage{runID: runID, event: event, ok: ok}
	}
}

func (model *Model) waitForQueueEvents() tea.Cmd {
	commands := make([]tea.Cmd, 0, len(model.queueSubs))
	for runID := range model.queueSubs {
		if model.queueWaiting[runID] {
			continue
		}
		model.queueWaiting[runID] = true
		commands = append(commands, waitForQueueEvent(model, runID))
	}
	if len(commands) == 0 {
		return nil
	}
	return tea.Batch(commands...)
}

func (model *Model) applyQueueEvent(message queueEventMessage) tea.Cmd {
	model.queueWaiting[message.runID] = false
	if !message.ok {
		if subscription := model.queueSubs[message.runID]; subscription != nil {
			subscription.Close()
			delete(model.queueSubs, message.runID)
		}
		delete(model.queueWaiting, message.runID)
		model.refreshQueue()
		return model.waitForQueueEvents()
	}
	event := message.event
	if event.Kind == agentruntime.EventRunning && model.activeRunID == "" {
		model.activeRunID = message.runID
	}
	switch event.Kind {
	case agentruntime.EventReasoningDelta:
		model.reasonings[message.runID] += event.Text
	case agentruntime.EventTextDelta:
		model.partials[message.runID] += event.Text
	case agentruntime.EventToolStarted:
		model.lines = append(model.lines, formatToolStarted(event))
	case agentruntime.EventToolFinished:
		model.lines = append(model.lines, formatToolFinished(event))
	case agentruntime.EventCompleted:
		text := event.Text
		if text == "" {
			text = model.partials[message.runID]
		}
		model.commitQueueReasoning(message.runID)
		if text != "" {
			model.lines = append(model.lines, "assistant: "+text)
		}
	case agentruntime.EventCanceled:
		model.commitQueueReasoning(message.runID)
		text := model.partials[message.runID]
		if text == "" {
			text = event.Text
		}
		if text != "" {
			model.lines = append(model.lines, "assistant (canceled): "+text)
		} else {
			model.lines = append(model.lines, "assistant (canceled)")
		}
	case agentruntime.EventFailed:
		model.commitQueueReasoning(message.runID)
		text := model.partials[message.runID]
		if text == "" {
			text = event.Text
		}
		if text != "" {
			model.lines = append(model.lines, "assistant (partial): "+text)
		}
		if event.Err != nil {
			model.lines = append(model.lines, "error: "+event.Err.Error())
		}
	}
	terminal := event.IsTerminal()
	if terminal {
		if model.seenNotifications != nil {
			model.seenNotifications[message.runID] = true
		}
		if subscription := model.queueSubs[message.runID]; subscription != nil {
			subscription.Close()
			delete(model.queueSubs, message.runID)
		}
		delete(model.queueWaiting, message.runID)
		delete(model.partials, message.runID)
		delete(model.reasonings, message.runID)
		if model.activeRunID == message.runID {
			model.activeRunID = ""
			for _, item := range model.queueItems {
				if item.Status == conversation.RunRunning {
					model.activeRunID = item.ID
					break
				}
			}
		}
	}
	model.refreshQueue()
	if model.queueMode && model.activeRunID == "" {
		for _, item := range model.queueItems {
			if item.Status == conversation.RunRunning {
				model.activeRunID = item.ID
				break
			}
		}
	}
	model.refreshViewport()
	if terminal {
		if len(model.queueItems) == 0 {
			model.state = stateIdle
			model.status = model.focusStatus()
		} else {
			model.state = stateRunning
		}
		return model.waitForQueueEvents()
	}
	return model.waitForQueueEvents()
}

func (model *Model) commitQueueReasoning(runID string) {
	if text := model.reasonings[runID]; text != "" {
		model.lines = append(model.lines, "reasoning: "+text)
		model.reasonings[runID] = ""
	}
}

func (model *Model) refreshQueue() {
	if !model.queueMode || model.queue == nil || model.username == "" || model.sessionID == "" {
		return
	}
	items, err := model.queue.List(context.Background(), model.username, model.sessionID, 100)
	if err != nil {
		model.status = "queue error: " + err.Error()
		return
	}
	model.queueItems = items
	activeStillRunning := false
	for _, item := range items {
		if item.ID == model.activeRunID && item.Status == conversation.RunRunning {
			activeStillRunning = true
			break
		}
	}
	if !activeStillRunning {
		model.activeRunID = ""
	}
	if model.activeRunID == "" {
		for _, item := range items {
			if item.Status == conversation.RunRunning {
				model.activeRunID = item.ID
				break
			}
		}
	}
	if len(items) > 0 {
		model.state = stateRunning
	} else if len(model.queueSubs) == 0 {
		model.state = stateIdle
	}
	if model.selected >= len(items) {
		model.selected = max(len(items)-1, 0)
	}
	model.ensureQueueSelectionVisible()
	for _, item := range items {
		if _, exists := model.queueSubs[item.ID]; !exists {
			model.subscribeQueueRun(item.ID)
		}
	}
	model.status = model.focusStatus()
}

func (model *Model) loadQueue() { model.refreshQueue() }

func (model *Model) selectedRunID() string {
	if len(model.queueItems) == 0 || model.selected < 0 || model.selected >= len(model.queueItems) {
		return ""
	}
	return model.queueItems[model.selected].ID
}

func (model *Model) selectQueue(delta int) {
	if len(model.queueItems) == 0 {
		return
	}
	model.selected = (model.selected + delta) % len(model.queueItems)
	if model.selected < 0 {
		model.selected = len(model.queueItems) - 1
	}
	model.ensureQueueSelectionVisible()
	model.status = model.focusStatus()
}

func (model *Model) ensureQueueSelectionVisible() {
	if len(model.queueItems) == 0 {
		model.selected = 0
		model.queueOffset = 0
		return
	}
	if model.selected < 0 {
		model.selected = 0
	}
	if model.selected >= len(model.queueItems) {
		model.selected = len(model.queueItems) - 1
	}
	visible := max(model.height/3, 3) - 1 // one row is reserved for counters
	if visible < 1 {
		visible = 1
	}
	if model.selected < model.queueOffset {
		model.queueOffset = model.selected
	}
	if model.selected >= model.queueOffset+visible {
		model.queueOffset = model.selected - visible + 1
	}
	maxOffset := max(len(model.queueItems)-visible, 0)
	if model.queueOffset > maxOffset {
		model.queueOffset = maxOffset
	}
}

func (model *Model) cancelSelected() tea.Cmd {
	if len(model.queueItems) == 0 || model.selected < 0 || model.selected >= len(model.queueItems) || model.queueItems[model.selected].Status != conversation.RunQueued {
		return nil
	}
	id := model.queueItems[model.selected].ID
	if id == "" {
		return nil
	}
	return func() tea.Msg {
		_, _ = model.queue.Cancel(context.Background(), model.username, model.sessionID, id)
		return nil
	}
}

func (model *Model) focusStatus() string {
	if !model.queueMode {
		return model.status
	}
	running, queued := 0, 0
	for _, item := range model.queueItems {
		if item.Status == conversation.RunRunning {
			running++
		}
		if item.Status == conversation.RunQueued {
			queued++
		}
	}
	focus := "input"
	if model.queueFocus {
		focus = "queue"
	}
	return fmt.Sprintf("running: %d · queued: %d · focus: %s", running, queued, focus)
}

// formatToolStarted 渲染工具调用开始时的全部细节。
func formatToolStarted(event agentruntime.Event) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "tool started: %s", event.Tool)
	if event.CallID != "" {
		fmt.Fprintf(&builder, "\ncall_id: %s", event.CallID)
	}
	if event.Arguments != "" {
		fmt.Fprintf(&builder, "\narguments: %s", event.Arguments)
	}
	return builder.String()
}

// formatToolFinished 渲染工具调用结束时的全部细节。
func formatToolFinished(event agentruntime.Event) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "tool completed: %s", event.Tool)
	if event.CallID != "" {
		fmt.Fprintf(&builder, "\ncall_id: %s", event.CallID)
	}
	if event.Result != "" {
		fmt.Fprintf(&builder, "\nresult: %s", event.Result)
	}
	return builder.String()
}

// transcript 返回展示用 transcript，包含进行中的部分响应。
func (model *Model) transcript() string {
	lines := append([]string(nil), model.lines...)
	if model.reasoning != "" {
		lines = append(lines, "reasoning: "+model.reasoning)
	}
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
	queueHeight := 0
	if model.queueMode {
		queueHeight = min(max(len(model.queueItems)+2, 3), max(model.height/3, 3))
	}
	model.viewport.Height = max(model.height-9-queueHeight, 3)
	model.input.SetWidth(max(model.width-6, 14))
	model.ensureQueueSelectionVisible()
	model.refreshViewport()
}
