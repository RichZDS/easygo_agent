package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/conversation"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

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
			debugAgenticMessage(run.context, partial)
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
		commitErr := run.commit(commitCtx, next, status, text, runError)
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
			commitErr = run.commit(commitCtx, cancelNext, status, text, "")
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
			fallbackErr := run.commit(fallbackCtx, failedNext, conversation.RunFailed, text, persistErr.Error())
			fallbackCancel()
			kind = EventFailed
			err = persistErr
			if fallbackErr != nil {
				err = fmt.Errorf("%w (failed-state commit: %v)", persistErr, fallbackErr)
			}
			telemetry.Logger(run.context).Error("persist queued conversation failed", zap.Error(err))
		}
		run.queueLease.Close()
	}
	run.cancel()
	run.terminal = Event{Kind: kind, Text: text, Err: err}
	run.finished = true
	run.trace.Finish(string(kind), err, zap.Int("output_messages", len(run.outputs)), zap.Int("output_bytes", len(text)))
	close(run.done)
}

func (run *agentRun) commit(ctx context.Context, next []*schema.AgenticMessage, status conversation.RunStatus, text, runError string) error {
	ctx, span := telemetry.Start(ctx, "persist", "commit_run", zap.String("target_status", string(status)), zap.Int("context_messages", len(next)))
	err := run.queueLease.CommitRun(ctx, next, status, run.outputs, text, runError)
	span.Finish("", err)
	return err
}
