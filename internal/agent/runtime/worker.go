package agentruntime

import (
	"context"
	"errors"
	"strconv"
	"time"

	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/conversation"

	"go.uber.org/zap"
)

func (manager *queueManager) recoverExpired() {
	defer manager.wg.Done()
	interval := manager.cfg.LeaseTTL / 3
	if interval <= 0 || interval > 10*time.Second {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-manager.workerContext().Done():
			return
		case now := <-ticker.C:
			if err := manager.store.RecoverExpired(manager.workerContext(), now.UTC()); err == nil {
				manager.signal()
			} else if manager.workerContext().Err() == nil {
				telemetry.Logger(manager.ctx).Error("queue recovery failed", zap.Error(err))
			}
		}
	}
}

func (manager *queueManager) worker(number int) {
	defer manager.wg.Done()
	workerID := "worker-" + strconv.Itoa(number)
	for {
		if manager.workerContext().Err() != nil {
			return
		}
		lease, err := manager.store.ClaimNext(manager.workerContext(), workerID, manager.cfg.LeaseTTL)
		if err == nil {
			manager.execute(lease)
			continue
		}
		if !errors.Is(err, conversation.ErrNoQueuedRun) && manager.workerContext().Err() != nil {
			return
		}
		if !errors.Is(err, conversation.ErrNoQueuedRun) {
			telemetry.Logger(manager.ctx).Error("queue claim failed", zap.String("worker_id", workerID), zap.Error(err))
		}
		timer := time.NewTimer(manager.cfg.PollInterval)
		select {
		case <-manager.workerContext().Done():
			timer.Stop()
			return
		case <-manager.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (manager *queueManager) workerContext() context.Context {
	if manager.workerCtx != nil {
		return manager.workerCtx
	}
	return manager.ctx
}

func (manager *queueManager) execute(lease conversation.RunLease) {
	if lease == nil {
		return
	}
	record := lease.Run()
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		lease.Close()
		return
	}
	runCtx, cancel := context.WithCancel(manager.ctx)
	run := NewClaimed(runCtx, manager.agent, lease, manager.memory)
	active := &activeRun{sessionID: record.SessionID, record: record, lease: lease, run: run}
	manager.active[record.ID] = active
	manager.mu.Unlock()
	// A DELETE can win the claim/registration race before the worker starts
	// consuming the model iterator. Observe the durable flag once here so such
	// a run is finalized as canceled without invoking the model; heartbeat
	// polling remains the fallback for later cancellation requests.
	checkTimeout := manager.cfg.PollInterval
	if checkTimeout <= 0 {
		checkTimeout = defaultQueuePoll
	}
	checkCtx, checkCancel := context.WithTimeout(context.Background(), checkTimeout)
	cancelRequested, checkErr := lease.CancelRequested(checkCtx)
	checkCancel()
	if checkErr != nil || cancelRequested {
		if checkErr != nil {
			telemetry.Logger(runCtx).Warn("queue cancellation check failed", zap.String("run_id", record.ID), zap.Error(checkErr))
		}
		run.Cancel()
	}
	defer func() {
		cancel()
		manager.mu.Lock()
		// An expired lease may be recovered and claimed by another worker
		// before this worker finishes. Do not let the stale worker delete the
		// newer active entry used by cancellation and shutdown.
		if current := manager.active[record.ID]; current == active {
			delete(manager.active, record.ID)
		}
		manager.mu.Unlock()
		manager.signal()
	}()
	manager.publish(Event{Kind: EventRunning, RunID: record.ID, Status: conversation.RunRunning, Position: 0})

	heartbeatStop := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go manager.heartbeat(run, lease, heartbeatStop, heartbeatDone)
	for {
		event := run.Next()
		manager.publish(event)
		if event.IsTerminal() {
			break
		}
	}
	close(heartbeatStop)
	<-heartbeatDone
	run.Close()
}

func (manager *queueManager) heartbeat(run Run, lease conversation.RunLease, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	interval := manager.cfg.LeaseTTL / 3
	if interval <= 0 || interval > 10*time.Second {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-manager.ctx.Done():
			return
		case <-ticker.C:
			heartbeatCtx, cancel := context.WithTimeout(context.Background(), interval)
			err := lease.Heartbeat(heartbeatCtx, manager.cfg.LeaseTTL)
			cancel()
			if err != nil {
				telemetry.Logger(manager.ctx).Warn("queue heartbeat failed", zap.String("run_id", lease.Run().ID), zap.Error(err))
				run.Cancel()
				return
			}
			checkCtx, checkCancel := context.WithTimeout(context.Background(), interval)
			requested, err := lease.CancelRequested(checkCtx)
			checkCancel()
			if err != nil || requested {
				if err != nil {
					telemetry.Logger(manager.ctx).Warn("queue cancellation check failed", zap.String("run_id", lease.Run().ID), zap.Error(err))
				}
				run.Cancel()
				return
			}
		}
	}
}

// Queue latency comes from durable timestamps, not event delivery to a client.
func queueWait(record conversation.RunRecord) float64 {
	if record.StartedAt == nil || record.CreatedAt.IsZero() {
		return 0
	}
	return max(0, float64(record.StartedAt.Sub(record.CreatedAt))/float64(time.Millisecond))
}
