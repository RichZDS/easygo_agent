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

// newEventStream 构造内存流，保证生产者不会被阻塞。
func newEventStream(cancel context.CancelFunc, logger *zap.Logger) *eventStream {
	stream := &eventStream{
		cancel: cancel,
		done:   make(chan struct{}),
		logger: logger,
	}
	stream.ready = sync.NewCond(&stream.mu)
	return stream
}

// Recv 等待一条事件，或在终态事件消费后返回 EOF。
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

// Close 取消进行中的运行，并等待生产者释放资源。
func (stream *eventStream) Close() error {
	stream.cancel()
	<-stream.done
	return nil
}

// emit 追加一条非终态事件；流已结束时直接丢弃。
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

// finish 恰好追加一条终态事件，并关闭生产者生命周期。
func (stream *eventStream) finish(event Event) {
	// closeProducer 关闭流并唤醒所有等待者。
	stream.finishOnce.Do(func() {
		stream.mu.Lock()
		stream.queue = append(stream.queue, event)
		stream.closed = true
		stream.ready.Broadcast()
		stream.mu.Unlock()
		close(stream.done)
	})
}

// abort 关闭从未返回给调用方的、组装失败的流。
func (stream *eventStream) abort() {
	// closeAbandoned 关闭未对外暴露的流。
	stream.finishOnce.Do(func() {
		stream.mu.Lock()
		stream.closed = true
		stream.ready.Broadcast()
		stream.mu.Unlock()
		close(stream.done)
	})
}
