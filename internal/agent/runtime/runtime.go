// Package agentruntime 持有一次对话会话，并把 Eino 输出投影为与框架无关的语义事件流。
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"easygo-agent/internal/logger"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

var (
	// ErrEmptyInput 表示一次运行没有可发送的用户文本。
	ErrEmptyInput = errors.New("agent input cannot be empty")
	// ErrRunInProgress 表示会话中已有一次尚未结束的运行。
	ErrRunInProgress = errors.New("agent run already in progress")
	// ErrAgentUnavailable 表示会话没有可调用的 Agent。
	ErrAgentUnavailable = errors.New("agent is unavailable")
	// ErrRunFinished 表示调用方在终态事件之后再次读取运行。
	ErrRunFinished = errors.New("agent run already finished")
)

// EventKind 标识 TUI 等调用方需要处理的一种运行事件。
type EventKind string

const (
	// EventTextDelta 表示助手文本增量。
	EventTextDelta EventKind = "text_delta"
	// EventToolStarted 表示工具调用开始。
	EventToolStarted EventKind = "tool_started"
	// EventToolFinished 表示工具调用结束。
	EventToolFinished EventKind = "tool_finished"
	// EventCompleted 表示运行成功结束。
	EventCompleted EventKind = "completed"
	// EventCanceled 表示运行被取消。
	EventCanceled EventKind = "canceled"
	// EventFailed 表示运行因错误失败。
	EventFailed EventKind = "failed"
)

// Event 是 Agent 运行投影出的语义事件。终态事件的 Text 是本次运行的完整助手文本。
type Event struct {
	// Kind 标识事件类型。
	Kind EventKind
	// Text 是文本增量；终态事件则为本次运行的完整助手文本。
	Text string
	// Tool 是工具名称，仅工具生命周期事件填充。
	Tool string
	// Err 仅 Failed 终态携带错误。
	Err error
}

// Conversation 持有对话历史，并保证同一时刻最多运行一个请求。
type Conversation interface {
	// Start 把用户输入加入历史，并启动一条启用流式输出的运行。
	Start(input string) (Run, error)
}

// Run 是一条正在进行的 Agent 运行。
// Next 阻塞到下一条事件；Cancel 可与 Next 并发调用。
// 一次运行恰好返回一个 Completed、Canceled 或 Failed 终态事件。
type Run interface {
	// Next 阻塞直到下一条语义事件可用。调用方应循环读取直到收到终态事件。
	Next() Event
	// Cancel 请求取消运行；终态仍由随后的 Next 返回。
	Cancel()
}

// Session 是基于 Eino TypedAgent 的进程内对话会话。
type Session struct {
	mu      sync.Mutex                             // 保护 history 与 active
	parent  context.Context                        // 每次 Run 的父 context；取消后后续运行一并停止
	agent   adk.TypedAgent[*schema.AgenticMessage] // 实际执行推理的 Eino Agent
	history []*schema.AgenticMessage               // 系统提示词、用户输入、完整助手回复；失败/取消的部分输出不写入
	active  bool                                   // 是否已有一次尚未结束的运行
}

// New 构造一个会话。系统提示词与完整助手回复由 Session 维护，不暴露给 TUI。
func New(parent context.Context, agent adk.TypedAgent[*schema.AgenticMessage], systemPrompt string) *Session {
	if parent == nil {
		parent = context.Background()
	}
	session := &Session{parent: parent, agent: agent}
	if prompt := strings.TrimSpace(systemPrompt); prompt != "" {
		session.history = append(session.history, schema.SystemAgenticMessage(prompt))
	}
	return session
}

// Start 把用户输入加入历史，并启动一条启用流式输出的 Eino 运行。
func (session *Session) Start(input string) (Run, error) {
	content := strings.TrimSpace(input)
	if content == "" {
		logger.Error("start agent run failed", zap.Error(ErrEmptyInput))
		return nil, ErrEmptyInput
	}

	session.mu.Lock()
	if session.agent == nil {
		session.mu.Unlock()
		logger.Error("start agent run failed", zap.Error(ErrAgentUnavailable))
		return nil, ErrAgentUnavailable
	}
	if session.active {
		session.mu.Unlock()
		logger.Error("start agent run failed", zap.Error(ErrRunInProgress))
		return nil, ErrRunInProgress
	}
	session.active = true
	history := append(slices.Clone(session.history), schema.UserAgenticMessage(content))
	parent := session.parent
	agent := session.agent
	session.mu.Unlock()

	logger.Debug("agent run starting",
		zap.String("input", content),
		zap.Int("history_len", len(history)),
		zap.Any("history", history),
	)

	runContext, cancel := context.WithCancel(parent)
	iterator := agent.Run(runContext, &adk.TypedAgentInput[*schema.AgenticMessage]{
		Messages:        slices.Clone(history),
		EnableStreaming: true,
	})
	if iterator == nil {
		cancel()
		session.release()
		logger.Error("start agent run failed", zap.Error(ErrAgentUnavailable), zap.Any("history", history))
		return nil, ErrAgentUnavailable
	}

	session.mu.Lock()
	session.history = history
	session.mu.Unlock()

	return &agentRun{
		context:       runContext,
		cancel:        cancel,
		iterator:      iterator,
		session:       session,
		startedTools:  make(map[string]struct{}),
		finishedTools: make(map[string]struct{}),
	}, nil
}

// complete 只在成功终态把完整助手文本写入下一轮历史。
func (session *Session) complete(text string) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if text != "" {
		session.history = append(session.history, assistantMessage(text))
	}
	session.active = false
}

// release 结束失败或取消的运行，不把部分助手文本写入历史。
func (session *Session) release() {
	session.mu.Lock()
	session.active = false
	session.mu.Unlock()
}

// agentRun 把 Eino 迭代器与消息流投影为语义事件，并实现 Run。
type agentRun struct {
	nextMu        sync.Mutex                                                       // 保证 Next 串行消费
	context       context.Context                                                  // 本次运行的可取消 context
	cancel        context.CancelFunc                                               // 取消本次运行
	iterator      *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] // Eino 运行事件迭代器
	messageStream *schema.StreamReader[*schema.AgenticMessage]                     // 当前正在消费的流式消息；非流式事件为 nil
	session       *Session                                                         // 所属会话，用于提交或丢弃历史
	pending       []Event                                                          // 已投影但尚未被 Next 取出的事件
	text          strings.Builder                                                  // 本次运行累计的完整助手文本
	startedTools  map[string]struct{}                                              // 已发出 EventToolStarted 的 CallID
	finishedTools map[string]struct{}                                              // 已发出 EventToolFinished 的 CallID
	finished      bool                                                             // 是否已返回终态事件
	streamLog     streamChunkLog                                                   // 当前消息流的 chunk，结束后再聚合打印
}

// Cancel 请求取消运行。终态仍由下一次 Next 返回。
func (run *agentRun) Cancel() {
	run.cancel()
}

// Next 隐藏 Eino 迭代器与消息流，并逐条返回稳定的语义事件。
func (run *agentRun) Next() Event {
	run.nextMu.Lock()
	defer run.nextMu.Unlock()
	event := run.next()
	debugSemanticEvent(event)
	return event
}

// next 在持锁前提下消费 Eino 输出并返回下一条语义事件。
func (run *agentRun) next() Event {
	if run.finished {
		return Event{Kind: EventFailed, Text: run.text.String(), Err: ErrRunFinished}
	}
	for {
		if event, ok := run.popPending(); ok {
			return event
		}
		if run.context.Err() != nil {
			return run.finish(EventCanceled, nil)
		}
		if run.messageStream != nil {
			if terminal := run.receiveMessageChunk(); terminal != nil {
				return *terminal
			}
			continue
		}

		event, ok := run.iterator.Next()
		if !ok {
			logger.Debug("eino iterator exhausted")
			return run.finish(EventCompleted, nil)
		}
		if run.context.Err() != nil {
			return run.finish(EventCanceled, nil)
		}
		if event == nil {
			logger.Debug("eino agent event is nil")
			continue
		}
		debugEinoEvent(event)
		if event.Err != nil {
			wrappedErr := fmt.Errorf("receive Eino agent event: %w", event.Err)
			logger.Error("agent run failed", zap.Error(wrappedErr))
			return run.finish(EventFailed, wrappedErr)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		output := event.Output.MessageOutput
		if output.IsStreaming && output.MessageStream != nil {
			output.MessageStream.SetAutomaticClose()
			run.messageStream = output.MessageStream
			continue
		}
		if output.Message != nil {
			debugAgenticMessage(output.Message)
			run.projectMessage(output.Message)
		}
	}
}

// receiveMessageChunk 消费当前 Eino 消息流；仅错误或取消会直接产生终态。
func (run *agentRun) receiveMessageChunk() *Event {
	message, err := run.messageStream.Recv()
	if err != nil {
		if run.context.Err() != nil {
			event := run.finish(EventCanceled, nil)
			return &event
		}
		if errors.Is(err, io.EOF) {
			run.closeMessageStream()
			return nil
		}
		wrappedErr := fmt.Errorf("receive Eino message stream: %w", err)
		logger.Error("agent message stream failed", zap.Error(wrappedErr))
		event := run.finish(EventFailed, wrappedErr)
		return &event
	}
	if message != nil {
		run.streamLog.append(message)
		run.projectMessage(message)
	}
	return nil
}

// projectMessage 把 AgenticMessage 内容块转成文本与 Tool 生命周期事件。
func (run *agentRun) projectMessage(message *schema.AgenticMessage) {
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.AssistantGenText != nil && block.AssistantGenText.Text != "" {
			text := block.AssistantGenText.Text
			run.text.WriteString(text)
			run.pending = append(run.pending, Event{Kind: EventTextDelta, Text: text})
		}
		if block.FunctionToolCall != nil {
			run.projectTool(EventToolStarted, block.FunctionToolCall.Name, block.FunctionToolCall.CallID)
		}
		if block.FunctionToolResult != nil {
			run.projectTool(EventToolFinished, block.FunctionToolResult.Name, block.FunctionToolResult.CallID)
		}
	}
}

// projectTool 按 CallID 去重，避免流式 ContentBlock 重复展示 Tool 状态。
func (run *agentRun) projectTool(kind EventKind, name, callID string) {
	key := callID
	if key == "" {
		key = name
	}
	if key == "" {
		return
	}
	seen := run.startedTools
	if kind == EventToolFinished {
		seen = run.finishedTools
	}
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	if name == "" {
		name = key
	}
	run.pending = append(run.pending, Event{Kind: kind, Tool: name})
}

// popPending 取出最早一条已投影、尚未交付的事件。
func (run *agentRun) popPending() (Event, bool) {
	if len(run.pending) == 0 {
		return Event{}, false
	}
	event := run.pending[0]
	run.pending[0] = Event{}
	run.pending = run.pending[1:]
	return event, true
}

// finish 释放本次运行资源，并恰好生成一个终态事件。
func (run *agentRun) finish(kind EventKind, err error) Event {
	run.finished = true
	run.closeMessageStream()
	run.cancel()
	text := run.text.String()
	if kind == EventCompleted {
		run.session.complete(text)
	} else {
		run.session.release()
	}
	return Event{Kind: kind, Text: text, Err: err}
}

// closeMessageStream 关闭当前消息流，并把已收集的 Streaming chunk 聚合后打印。
func (run *agentRun) closeMessageStream() {
	run.streamLog.flush()
	if run.messageStream == nil {
		return
	}
	run.messageStream.Close()
	run.messageStream = nil
}

// assistantMessage 用完整助手文本构造一条写入历史的 AgenticMessage。
func assistantMessage(text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: text}),
		},
	}
}

// 编译期断言 Session 与 agentRun 分别实现 Conversation 与 Run。
var (
	_ Conversation = (*Session)(nil)
	_ Run          = (*agentRun)(nil)
)
