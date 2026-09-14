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
	"easygo-agent/internal/usermemory"

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
	ErrQueueClosed      = errors.New("queue manager is closed")
	// ErrRunFinished 表示调用方在终态事件之后再次读取运行。
	ErrRunFinished = errors.New("agent run already finished")
)

// EventKind 标识 TUI 等调用方需要处理的一种运行事件。
type EventKind string

const (
	// EventTextDelta 表示助手文本增量。
	EventTextDelta EventKind = "text_delta"
	// EventReasoningDelta 表示 reasoning 文本增量。
	EventReasoningDelta EventKind = "reasoning_delta"
	// EventToolStarted 表示工具调用开始，携带完整 name、call_id 与 arguments。
	EventToolStarted EventKind = "tool_started"
	// EventToolFinished 表示工具调用结束，携带完整 name、call_id 与 result。
	EventToolFinished EventKind = "tool_finished"
	// EventCompleted 表示运行成功结束。
	EventCompleted EventKind = "completed"
	// EventCanceled 表示运行被取消。
	EventCanceled EventKind = "canceled"
	// EventFailed 表示运行因错误失败。
	EventFailed EventKind = "failed"
	// EventQueued 表示请求已持久化并等待 worker。
	EventQueued EventKind = "queued"
	// EventRunning 表示请求已被 worker claim。
	EventRunning     EventKind = "running"
	EventCompressing EventKind = "compressing"
	EventCompressed  EventKind = "compressed"
)

// Event 是 Agent 运行投影出的语义事件。终态事件的 Text 是本次运行的完整助手文本。
type Event struct {
	// Queue metadata is populated for runs executed by QueueManager.
	RunID    string                 `json:"run_id,omitempty"`
	Status   conversation.RunStatus `json:"status,omitempty"`
	Position int                    `json:"position,omitempty"`
	// Kind 标识事件类型。
	Kind EventKind
	// Text 是文本或 reasoning 增量；终态事件则为本次运行的完整助手文本。
	Text string
	// Tool 是工具名称，仅工具生命周期事件填充。
	Tool string
	// CallID 是工具调用 ID。
	CallID string
	// Arguments 是工具调用的完整参数。
	Arguments string
	// Result 是工具返回的完整结果。
	Result string
	// Err 仅 Failed 终态携带错误。
	Err error
}

// Run 是一条正在进行的 Agent 运行。
// Next 串行交付事件，最后交付一次终态；Cancel 和 Close 可与 Next 并发调用。
// 取消会自动完成持久化与资源释放，无需调用方继续消费事件。
type Run interface {
	// Next 阻塞直到下一条语义事件可用。调用方应循环读取直到收到终态事件。
	Next() Event
	// Cancel 非阻塞地请求取消。终态仍可由 Next 读取。
	Cancel()
	// Close 取消尚未结束的运行，等待持久化与资源释放，并返回最终结果。
	// 可重复调用；不会消费 Next 的事件，已完成的结果不会被改为 canceled。
	Close() Event
}

// ClaimQueuedRun enqueues input, claims the resulting row, and returns the
// same Run QueueManager workers construct. Tests and one-shot evals use this
// so persist always goes through RunLease.
func ClaimQueuedRun(ctx context.Context, store conversation.QueueStore, agent adk.TypedAgent[*schema.AgenticMessage], user, sessionID, input string, memory ...conversation.MemoryStore) (Run, error) {
	if store == nil {
		return nil, ErrStoreUnavailable
	}
	if _, err := store.Enqueue(ctx, user, sessionID, input, ""); err != nil {
		return nil, err
	}
	lease, err := store.ClaimNext(ctx, "claimed-run", time.Minute)
	if err != nil {
		return nil, err
	}
	return NewClaimed(ctx, agent, lease, memory...), nil
}

// NewClaimed constructs a run from a lease already claimed by QueueManager.
// It bypasses Store.Begin so claim ownership and the final transaction remain
// one continuous boundary.
func NewClaimed(parent context.Context, agent adk.TypedAgent[*schema.AgenticMessage], lease conversation.RunLease, memory ...conversation.MemoryStore) Run {
	if parent == nil {
		parent = context.Background()
	}
	if lease == nil {
		cancel := func() {}
		return &agentRun{
			agent:     agent,
			context:   parent,
			cancel:    cancel,
			done:      closedRunChannel(),
			finished:  true,
			projector: newEventProjector(),
			terminal:  Event{Kind: EventFailed, Err: ErrStoreUnavailable},
		}
	}
	record := lease.Run()
	runContext, cancel := context.WithCancel(parent)
	runContext = WithInvocationIdentity(runContext, InvocationIdentity{SessionID: record.SessionID, RunID: record.ID})
	capture := &stateCapture{}
	run := &agentRun{
		context:    context.WithValue(runContext, captureKey{}, capture),
		cancel:     cancel,
		input:      record.Input,
		agent:      agent,
		queueLease: lease,
		record:     record,
		capture:    capture,
		done:       make(chan struct{}),
		projector:  newEventProjector(),
	}
	if len(memory) > 0 {
		run.memory = memory[0]
	}
	context.AfterFunc(run.context, func() {
		run.nextMu.Lock()
		defer run.nextMu.Unlock()
		run.finalize(EventCanceled, nil)
	})
	return run
}

func closedRunChannel() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

// agentRun 把 Eino 迭代器与消息流投影为语义事件，并实现 Run。
type agentRun struct {
	input         string
	inputMessages []*schema.AgenticMessage
	outputs       []*schema.AgenticMessage
	chunks        []*schema.AgenticMessage
	queueLease    conversation.RunLease
	record        conversation.RunRecord
	agent         adk.TypedAgent[*schema.AgenticMessage]
	capture       *stateCapture
	nextMu        sync.Mutex                                                       // 保证 Next 串行消费
	context       context.Context                                                  // 本次运行的可取消 context
	cancel        context.CancelFunc                                               // 取消本次运行
	iterator      *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] // Eino 运行事件迭代器
	messageStream *schema.StreamReader[*schema.AgenticMessage]                     // 当前正在消费的流式消息；非流式事件为 nil
	projector     *EventProjector
	finished      bool                  // 是否已完成持久化与资源释放
	terminal      Event                 // finished 后不可变，供 Close 重复读取
	terminalRead  bool                  // Next 是否已交付终态
	done          chan struct{}         // terminal 就绪、资源释放后关闭
	streamLog     logger.StreamChunkLog // 当前消息流的 chunk，结束后再聚合打印
	memory        conversation.MemoryStore
}

// Cancel requests cancellation; runtime owns cleanup independently of Next.
func (run *agentRun) Cancel() {
	run.cancel()
}

func (run *agentRun) Close() Event {
	run.cancel()
	<-run.done
	return run.decorate(run.terminal)
}

// Next 隐藏 Eino 迭代器与消息流，并逐条返回稳定的语义事件。
func (run *agentRun) Next() Event {
	run.nextMu.Lock()
	defer run.nextMu.Unlock()
	event := run.next()
	event = run.decorate(event)
	debugSemanticEvent(event)
	return event
}

// next 在持锁前提下消费 Eino 输出并返回下一条语义事件。
func (run *agentRun) next() Event {
	if event, ok := run.projector.popPending(); ok {
		return event
	}
	if run.finished {
		return run.readTerminal()
	}
	for {
		if event, ok := run.projector.popPending(); ok {
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
		if run.context.Err() != nil {
			return run.finish(EventCanceled, nil)
		}
		if !ok {
			logger.Debug("eino iterator exhausted")
			return run.finish(EventCompleted, nil)
		}
		if event == nil {
			logger.Debug("eino agent event is nil")
			continue
		}
		logger.DebugEinoEvent(event)
		if projected, ok := run.projector.ProjectCompression(event); ok {
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
			logger.DebugAgenticMessage(output.Message)
			run.outputs = append(run.outputs, output.Message)
			run.projector.ProjectMessage(output.Message)
			run.projector.flushToolDraft()
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
		run.streamLog.Append(message)
		run.projector.ProjectMessage(message)
	}
	return nil
}

// finish 释放本次运行资源，先交付尚未发出的 tool 事件，再返回终态事件。
func (run *agentRun) finish(kind EventKind, err error) Event {
	run.finalize(kind, err)
	if event, ok := run.projector.popPending(); ok {
		return event
	}
	return run.readTerminal()
}

// readTerminal is called under nextMu; Close observes the same immutable result
// without consuming the event reserved for Next.
func (run *agentRun) readTerminal() Event {
	if run.terminalRead {
		return Event{Kind: EventFailed, Text: run.terminal.Text, Err: ErrRunFinished}
	}
	run.terminalRead = true
	return run.terminal
}

// finalize owns the entire completion protocol. All callers hold nextMu, so
// cancellation, Close and iterator completion can only commit/release once.
func (run *agentRun) finalize(kind EventKind, err error) {
	if run.finished {
		return
	}
	// Preserve received partial native output in the audit record only.
	if len(run.chunks) > 0 {
		if partial, concatErr := schema.ConcatAgenticMessages(run.chunks); concatErr == nil {
			run.outputs = append(run.outputs, partial)
		}
		run.chunks = nil
	}
	run.closeMessageStream()
	text := run.projector.Text()
	if run.queueLease != nil && run.inputMessages == nil {
		run.inputMessages = append(slices.Clone(run.queueLease.Messages()), schema.UserAgenticMessage(run.input))
	}
	next := run.inputMessages
	if kind == EventCompleted {
		next = run.capture.get()
		if next == nil {
			next = append(slices.Clone(run.inputMessages), run.outputs...)
		}
	}
	if run.queueLease != nil {
		status := conversation.RunFailed
		switch kind {
		case EventCompleted:
			status = conversation.RunCompleted
		case EventCanceled:
			status = conversation.RunCanceled
		}
		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(run.context), 10*time.Second)
		runError := ""
		if err != nil {
			runError = err.Error()
		}
		commitErr := run.queueLease.CommitRun(commitCtx, next, status, run.outputs, text, runError)
		cancel()
		// A cancellation request that wins immediately before the final
		// transaction must not be silently cleared by a successful completion.
		// Retry the same atomic commit as canceled, using the pre-run context.
		if errors.Is(commitErr, conversation.ErrCancellationRequested) && status == conversation.RunCompleted {
			status = conversation.RunCanceled
			kind = EventCanceled
			err = nil
			cancelNext := run.inputMessages
			commitCtx, cancel = context.WithTimeout(context.WithoutCancel(run.context), 10*time.Second)
			commitErr = run.queueLease.CommitRun(commitCtx, cancelNext, status, run.outputs, text, "")
			cancel()
		}
		if commitErr != nil {
			persistErr := fmt.Errorf("persist queued conversation: %w", commitErr)
			// A failed transaction normally leaves the lease claim intact. Make a
			// best effort to persist a failed terminal row before releasing it;
			// otherwise Close deliberately requeues the work for recovery.
			failedNext := next
			if kind == EventCompleted {
				failedNext = run.inputMessages
			}
			fallbackCtx, fallbackCancel := context.WithTimeout(context.WithoutCancel(run.context), 10*time.Second)
			fallbackErr := run.queueLease.CommitRun(fallbackCtx, failedNext, conversation.RunFailed, run.outputs, text, persistErr.Error())
			fallbackCancel()
			kind = EventFailed
			err = persistErr
			if fallbackErr != nil {
				err = fmt.Errorf("%w (failed-state commit: %v)", persistErr, fallbackErr)
			}
			logger.Error("persist queued conversation failed", zap.Error(err))
		}
		run.queueLease.Close()
	}
	run.cancel()
	run.terminal = Event{Kind: kind, Text: text, Err: err}
	run.finished = true
	close(run.done)
}

func (run *agentRun) initialize() error {
	if run.queueLease == nil {
		return ErrStoreUnavailable
	}
	run.inputMessages = append(slices.Clone(run.queueLease.Messages()), schema.UserAgenticMessage(run.input))
	memoryContext, recallErr := recallMemoryPrompt(run.context, run.memory, run.record.Username)
	if recallErr != nil {
		return recallErr
	}
	modelInput := make([]*schema.AgenticMessage, 0, len(run.inputMessages)+1)
	if memoryContext != "" {
		modelInput = append(modelInput, schema.SystemAgenticMessage(memoryContext))
	}
	modelInput = append(modelInput, DropPersistedSystemMessages(run.inputMessages)...)
	if run.agent == nil {
		return ErrAgentUnavailable
	}
	run.iterator = run.agent.Run(run.context, &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: modelInput, EnableStreaming: true})
	if run.iterator == nil {
		return ErrAgentUnavailable
	}
	return nil
}

// DropPersistedSystemMessages removes system rows from stored context before
// the next Run. Deep Agent re-injects Instruction; leftover system messages
// would duplicate it. Extracted theme notes must therefore be user messages.
func DropPersistedSystemMessages(messages []*schema.AgenticMessage) []*schema.AgenticMessage {
	out := make([]*schema.AgenticMessage, 0, len(messages))
	for _, message := range messages {
		if message != nil && message.Role != schema.AgenticRoleTypeSystem {
			out = append(out, message)
		}
	}
	return out
}

func recallMemoryPrompt(ctx context.Context, store conversation.MemoryStore, username string) (string, error) {
	if store == nil || strings.TrimSpace(username) == "" {
		return "", nil
	}
	memories, err := store.Recall(ctx, username, conversation.MaxProfileMemories)
	if err != nil {
		return "", fmt.Errorf("recall user memory: %w", err)
	}
	return usermemory.Prompt(memories), nil
}

func (run *agentRun) decorate(event Event) Event {
	if run.queueLease == nil {
		return event
	}
	event.RunID = run.record.ID
	event.Position = run.record.Position
	switch event.Kind {
	case EventQueued:
		event.Status = conversation.RunQueued
		event.Position = run.record.Position
	case EventCompleted:
		event.Status = conversation.RunCompleted
		event.Position = 0
	case EventCanceled:
		event.Status = conversation.RunCanceled
		event.Position = 0
	case EventFailed:
		event.Status = conversation.RunFailed
		event.Position = 0
	default:
		event.Status = conversation.RunRunning
		event.Position = 0
	}
	return event
}

// closeMessageStream 关闭当前消息流，发出已聚合的 tool call，并打印完整 AgenticMessage。
func (run *agentRun) closeMessageStream() {
	run.projector.flushToolDraft()
	run.streamLog.Flush()
	if run.messageStream == nil {
		return
	}
	run.messageStream.Close()
	run.messageStream = nil
}

var _ Run = (*agentRun)(nil)
