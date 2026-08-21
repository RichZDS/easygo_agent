package gateway

import (
	"context"
	"io"
	"sync"

	"go.uber.org/zap"
)

type eventStream struct {
	mu         sync.Mutex
	ready      *sync.Cond
	queue      []Event
	closed     bool
	cancel     context.CancelFunc
	done       chan struct{}
	finishOnce sync.Once
	logger     *zap.Logger
}

// newEventStream constructs an in-memory stream that cannot block its producer.
func newEventStream(cancel context.CancelFunc, logger *zap.Logger) *eventStream {
	stream := &eventStream{
		cancel: cancel,
		done:   make(chan struct{}),
		logger: logger,
	}
	stream.ready = sync.NewCond(&stream.mu)
	return stream
}

// Recv waits for one event or returns EOF after the terminal event is consumed.
func (stream *eventStream) Recv() (Event, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for len(stream.queue) == 0 && !stream.closed {
		stream.ready.Wait()
	}
	if len(stream.queue) == 0 {
		err := io.EOF
		stream.logger.Error("receive Gateway event finished", zap.String("stage", "stream_exhausted"), zap.Error(err))
		return Event{}, err
	}
	event := stream.queue[0]
	stream.queue[0] = Event{}
	stream.queue = stream.queue[1:]
	return event, nil
}

// Close cancels an active run and waits until its producer releases resources.
func (stream *eventStream) Close() error {
	stream.cancel()
	<-stream.done
	return nil
}

// emit appends a non-terminal event unless the stream already finished.
func (stream *eventStream) emit(event Event) bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed {
		return false
	}
	stream.queue = append(stream.queue, event)
	stream.ready.Signal()
	return true
}

// finish appends exactly one terminal event and closes the producer lifecycle.
func (stream *eventStream) finish(event Event) {
	stream.finishOnce.Do(func() {
		stream.mu.Lock()
		stream.queue = append(stream.queue, event)
		stream.closed = true
		stream.ready.Broadcast()
		stream.mu.Unlock()
		close(stream.done)
	})
}

// abort closes setup-failed streams that were never returned to a caller.
func (stream *eventStream) abort() {
	stream.finishOnce.Do(func() {
		stream.mu.Lock()
		stream.closed = true
		stream.ready.Broadcast()
		stream.mu.Unlock()
		close(stream.done)
	})
}
