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
	"time"

	"easygo-agent/internal/conversation"
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
	ErrStoreUnavailable = errors.New("conversation store is unavailable")
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
	EventFailed      EventKind = "failed"
	EventCompressing EventKind = "compressing"
	EventCompressed  EventKind = "compressed"
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

// Session binds an Eino Agent to a user-scoped Store; it owns no second history cache.
type Session struct {
	mu       sync.Mutex                             // 保护 active
	parent   context.Context                        // 每次 Run 的父 context；取消后后续运行一并停止
	agent    adk.TypedAgent[*schema.AgenticMessage] // 实际执行推理的 Eino Agent
	active   bool                                   // 是否已有一次尚未结束的运行
	store    conversation.Store
	username string
	id       string
}

// NewStored binds a runtime to a durable, user-scoped conversation.
func NewStored(parent context.Context, agent adk.TypedAgent[*schema.AgenticMessage], store conversation.Store, username, id string) *Session {
	return &Session{parent: parent, agent: agent, store: store, username: username, id: id}
}

// Start 把用户输入加入历史，并启动一条启用流式输出的 Eino 运行。
func (session *Session) Start(input string) (Run, error) {
	return session.StartContext(session.parent, input)
}

// StartContext only reserves this runtime. I/O and inference happen in Next,
// so the CLI remains responsive and Cancel also works during history loading.
func (session *Session) StartContext(parent context.Context, input string) (Run, error) {
	content := strings.TrimSpace(input)
	if content == "" {
		logger.Error("start agent run failed", zap.Error(ErrEmptyInput))
		return nil, ErrEmptyInput
	}

	session.mu.Lock()
	if session.store == nil {
		session.mu.Unlock()
		return nil, ErrStoreUnavailable
	}
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
	session.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	runContext, cancel := context.WithCancel(parent)
	return &agentRun{
		context:       runContext,
		cancel:        cancel,
		input:         content,
		capture:       &stateCapture{},
		session:       session,
		startedTools:  make(map[string]struct{}),
		finishedTools: make(map[string]struct{}),
	}, nil
}

// release releases the local run reservation after persistence has finished.
func (session *Session) release() {
	session.mu.Lock()
	session.active = false
	session.mu.Unlock()
}

// agentRun 把 Eino 迭代器与消息流投影为语义事件，并实现 Run。
type agentRun struct {
	input         string
	inputMessages []*schema.AgenticMessage
	outputs       []*schema.AgenticMessage
	chunks        []*schema.AgenticMessage
	lease         conversation.Lease
	capture       *stateCapture
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
		if run.iterator == nil {
			if err := run.initialize(); err != nil {
				if run.context.Err() != nil {
					return run.finish(EventCanceled, nil)
				}
				return run.finish(EventFailed, err)
			}
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
		if projected, ok := compressionEvent(event); ok {
			return projected
		}
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
			run.outputs = append(run.outputs, output.Message)
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
			if len(run.chunks) > 0 {
				full, concatErr := schema.ConcatAgenticMessages(run.chunks)
				if concatErr != nil {
					event := run.finish(EventFailed, concatErr)
					return &event
				}
				run.outputs = append(run.outputs, full)
			}
			run.chunks = nil
			run.closeMessageStream()
			return nil
		}
		wrappedErr := fmt.Errorf("receive Eino message stream: %w", err)
		logger.Error("agent message stream failed", zap.Error(wrappedErr))
		event := run.finish(EventFailed, wrappedErr)
		return &event
	}
	if message != nil {
		run.chunks = append(run.chunks, message)
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
	// Preserve received partial native output in the audit record only.
	if len(run.chunks) > 0 {
		if partial, concatErr := schema.ConcatAgenticMessages(run.chunks); concatErr == nil {
			run.outputs = append(run.outputs, partial)
		}
		run.chunks = nil
	}
	run.closeMessageStream()
	text := run.text.String()
	next := run.inputMessages
	if kind == EventCompleted {
		next = run.capture.get()
		if next == nil {
			next = append(slices.Clone(run.inputMessages), run.outputs...)
		}
	}
	if run.lease != nil {
		// Canceled requests still get an audit record, using a bounded cleanup context.
		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(run.context), 10*time.Second)
		audit := append([]*schema.AgenticMessage{schema.UserAgenticMessage(run.input)}, run.outputs...)
		commitErr := run.lease.Commit(commitCtx, next, conversation.Turn{Status: string(kind), Messages: audit})
		cancel()
		run.lease.Close()
		if commitErr != nil {
			kind = EventFailed
			err = fmt.Errorf("persist conversation: %w", commitErr)
		}
	}
	run.session.release()
	run.cancel()
	return Event{Kind: kind, Text: text, Err: err}
}

func (run *agentRun) initialize() error {
	lease, err := run.session.store.Begin(run.context, run.session.username, run.session.id)
	if err != nil {
		return err
	}
	run.lease = lease
	run.inputMessages = append(slices.Clone(lease.Messages()), schema.UserAgenticMessage(run.input))
	run.context = context.WithValue(run.context, captureKey{}, run.capture)
	// Deep Agent injects the current system instruction on every Run.
	modelInput := make([]*schema.AgenticMessage, 0, len(run.inputMessages))
	for _, m := range run.inputMessages {
		if m != nil && m.Role != schema.AgenticRoleTypeSystem {
			modelInput = append(modelInput, m)
		}
	}
	run.iterator = run.session.agent.Run(run.context, &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: modelInput, EnableStreaming: true})
	if run.iterator == nil {
		return ErrAgentUnavailable
	}
	return nil
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

// 编译期断言 Session 与 agentRun 分别实现 Conversation 与 Run。
var (
	_ Conversation = (*Session)(nil)
	_ Run          = (*agentRun)(nil)
)
