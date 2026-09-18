// Package agentruntime 持有一次对话会话，并把 Eino 输出投影为与框架无关的语义事件流。
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/conversation"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

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
	runContext = WithInvocationIdentity(runContext, InvocationIdentity{Username: record.Username, Internal: record.Source != "", SessionID: record.SessionID, RunID: record.ID})
	runContext, span := telemetry.StartRun(runContext, telemetry.Identity{SessionID: record.SessionID, RunID: record.ID, WorkerID: record.WorkerID}, zap.Float64("queue_wait_ms", queueWait(record)))
	capture := &stateCapture{}
	run := &agentRun{
		context:    context.WithValue(runContext, captureKey{}, capture),
		cancel:     cancel,
		input:      record.Input,
		agent:      agent,
		queueLease: lease,
		record:     record,
		capture:    capture,
		trace:      span,
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
	finished      bool            // 是否已完成持久化与资源释放
	terminal      Event           // finished 后不可变，供 Close 重复读取
	terminalRead  bool            // Next 是否已交付终态
	done          chan struct{}   // terminal 就绪、资源释放后关闭
	trace         *telemetry.Span // one execution, finalized even when nobody reads Next
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
	debugSemanticEvent(run.context, event)
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
			telemetry.Logger(run.context).Debug("eino iterator exhausted")
			return run.finish(EventCompleted, nil)
		}
		if event == nil {
			telemetry.Logger(run.context).Debug("eino agent event is nil")
			continue
		}
		debugEinoEvent(run.context, event)
		if projected, ok := run.projector.ProjectCompression(event); ok {
			return projected
		}
		if event.Err != nil {
			wrappedErr := fmt.Errorf("receive Eino agent event: %w", event.Err)
			telemetry.Logger(run.context).Error("agent run failed", zap.Error(wrappedErr))
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
			debugAgenticMessage(run.context, output.Message)
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
				debugAgenticMessage(run.context, full)
			}
			run.chunks = nil
			run.closeMessageStream()
			return nil
		}
		wrappedErr := fmt.Errorf("receive Eino message stream: %w", err)
		telemetry.Logger(run.context).Error("agent message stream failed", zap.Error(wrappedErr))
		event := run.finish(EventFailed, wrappedErr)
		return &event
	}
	if message != nil {
		run.chunks = append(run.chunks, message)
		run.projector.ProjectMessage(message)
	}
	return nil
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

// closeMessageStream closes the current stream and flushes pending tool calls.
func (run *agentRun) closeMessageStream() {
	run.projector.flushToolDraft()
	if run.messageStream == nil {
		return
	}
	run.messageStream.Close()
	run.messageStream = nil
}

var _ Run = (*agentRun)(nil)
