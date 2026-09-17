package agentruntime

import (
	"context"
	"sync"
	"time"

	"easygo-agent/internal/conversation"
)

type subscriptionKey struct {
	user      string
	sessionID string
	runID     string
}

type runSubscription struct {
	manager      *queueManager
	key          subscriptionKey
	ch           chan Event
	notify       chan struct{}
	done         chan struct{}
	closedSignal chan struct{}
	mu           sync.Mutex
	pending      []Event
	closed       bool
	drain        bool
	doneClosed   bool
	hasState     bool
	lastStatus   conversation.RunStatus
	lastPosition int
	lastText     string
	lastError    string
}

func (manager *queueManager) Subscribe(ctx context.Context, user, sessionID, runID string) (Subscription, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if manager.store == nil {
		return nil, ErrStoreUnavailable
	}
	key := subscriptionKey{user: user, sessionID: sessionID, runID: runID}
	subscription := newRunSubscription(manager, key)
	manager.mu.Lock()
	if manager.closed || manager.ctx.Err() != nil {
		subscription.shutdown(false)
		manager.mu.Unlock()
		return nil, ErrQueueClosed
	}
	if manager.subscribers[key.runID] == nil {
		manager.subscribers[key.runID] = make(map[*runSubscription]struct{})
	}
	manager.subscribers[key.runID][subscription] = struct{}{}
	// Read current state while registered. Events produced after this point are
	// queued behind the initial snapshot and cannot be missed.
	record, err := manager.store.GetRun(ctx, user, sessionID, runID)
	if err != nil {
		delete(manager.subscribers[key.runID], subscription)
		subscription.shutdown(false)
		manager.mu.Unlock()
		return nil, wrapStoreError(err)
	}
	subscription.enqueue(eventForRecord(record))
	terminal := isRunTerminal(record.Status)
	if terminal {
		// Keep the subscription registered until its dispatcher drains the
		// initial terminal event. Manager.Close can therefore still force-stop
		// an observer that never reads from its channel.
		subscription.shutdown(true)
	}
	manager.mu.Unlock()
	if terminal {
		return subscription, nil
	}
	go manager.watchSubscription(ctx, subscription)
	return subscription, nil
}

// watchSubscription provides the cross-process fallback promised by the
// queue contract. Runtime events are deliberately in-memory, so a subscriber
// attached to a different application process may only observe durable state;
// polling is enough to deliver a later running/terminal transition without
// introducing a persistent event log or PostgreSQL NOTIFY dependency.
func (manager *queueManager) watchSubscription(ctx context.Context, subscription *runSubscription) {
	interval := manager.cfg.PollInterval
	if interval <= 0 {
		interval = defaultQueuePoll
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			subscription.Close()
			return
		case <-manager.ctx.Done():
			subscription.Close()
			return
		case <-subscription.closedSignal:
			return
		case <-ticker.C:
			pollCtx, cancel := context.WithTimeout(ctx, interval)
			record, err := manager.store.GetRun(pollCtx, subscription.key.user, subscription.key.sessionID, subscription.key.runID)
			cancel()
			if err != nil {
				if ctx.Err() != nil || manager.ctx.Err() != nil {
					return
				}
				continue
			}
			event := eventForRecord(record)
			subscription.enqueue(event)
			if event.IsTerminal() {
				subscription.shutdown(true)
				return
			}
		}
	}
}

func (manager *queueManager) publish(event Event) {
	if event.RunID == "" {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.publishLocked(event)
}

func (manager *queueManager) publishLocked(event Event) {
	if event.RunID == "" {
		return
	}
	for subscription := range manager.subscribers[event.RunID] {
		subscription.enqueue(event)
		if event.IsTerminal() {
			// Let dispatch drain the terminal event and remove itself. Keeping
			// it in the registry gives Close a way to interrupt a blocked
			// observer.
			subscription.shutdown(true)
		}
	}
}

func (manager *queueManager) subscriptionFinished(subscription *runSubscription) {
	if manager == nil || subscription == nil {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if subscriptions := manager.subscribers[subscription.key.runID]; subscriptions != nil {
		delete(subscriptions, subscription)
		if len(subscriptions) == 0 {
			delete(manager.subscribers, subscription.key.runID)
		}
	}
}

func (manager *queueManager) removeSubscriptionLocked(subscription *runSubscription, drain bool) {
	delete(manager.subscribers[subscription.key.runID], subscription)
	if len(manager.subscribers[subscription.key.runID]) == 0 {
		delete(manager.subscribers, subscription.key.runID)
	}
	subscription.shutdown(drain)
}

func newRunSubscription(manager *queueManager, key subscriptionKey) *runSubscription {
	subscription := &runSubscription{
		manager:      manager,
		key:          key,
		ch:           make(chan Event, 16),
		notify:       make(chan struct{}, 1),
		done:         make(chan struct{}),
		closedSignal: make(chan struct{}),
	}
	go subscription.dispatch()
	return subscription
}

func (subscription *runSubscription) enqueue(event Event) {
	subscription.mu.Lock()
	if subscription.closed {
		subscription.mu.Unlock()
		return
	}
	if status, ok := durableEventState(event); ok {
		errText := ""
		if event.Err != nil {
			errText = event.Err.Error()
		}
		if subscription.hasState && subscription.lastStatus == status && subscription.lastPosition == event.Position && subscription.lastText == event.Text && subscription.lastError == errText {
			subscription.mu.Unlock()
			return
		}
		subscription.hasState = true
		subscription.lastStatus = status
		subscription.lastPosition = event.Position
		subscription.lastText = event.Text
		subscription.lastError = errText
	}
	subscription.pending = append(subscription.pending, event)
	subscription.mu.Unlock()
	select {
	case subscription.notify <- struct{}{}:
	default:
	}
}

func durableEventState(event Event) (conversation.RunStatus, bool) {
	var inferred conversation.RunStatus
	switch event.Kind {
	case EventQueued:
		inferred = conversation.RunQueued
	case EventRunning:
		inferred = conversation.RunRunning
	case EventCompleted:
		inferred = conversation.RunCompleted
	case EventFailed:
		inferred = conversation.RunFailed
	case EventCanceled:
		inferred = conversation.RunCanceled
	default:
		return "", false
	}
	status := event.Status
	if status == "" {
		status = inferred
	}
	switch status {
	case conversation.RunQueued, conversation.RunRunning, conversation.RunCompleted, conversation.RunFailed, conversation.RunCanceled:
		return status, true
	default:
		return "", false
	}
}

func (subscription *runSubscription) isClosed() bool {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	return subscription.closed
}

func (subscription *runSubscription) shutdown(drain bool) {
	subscription.mu.Lock()
	if subscription.closed {
		// A terminal publication starts a graceful drain. An explicit Close
		// may subsequently force-stop a client that never reads the channel;
		// closing done also unblocks a dispatcher stuck on a full channel.
		if !drain && !subscription.doneClosed {
			subscription.drain = false
			close(subscription.done)
			subscription.doneClosed = true
		}
		subscription.mu.Unlock()
		select {
		case subscription.notify <- struct{}{}:
		default:
		}
		return
	}
	subscription.closed = true
	subscription.drain = drain
	close(subscription.closedSignal)
	if !drain {
		subscription.pending = nil
		close(subscription.done)
		subscription.doneClosed = true
	}
	subscription.mu.Unlock()
	select {
	case subscription.notify <- struct{}{}:
	default:
	}
}

func (subscription *runSubscription) dispatch() {
	if subscription.manager != nil {
		defer subscription.manager.subscriptionFinished(subscription)
	}
	defer close(subscription.ch)
	for {
		subscription.mu.Lock()
		if len(subscription.pending) == 0 {
			closed := subscription.closed
			subscription.mu.Unlock()
			if closed {
				return
			}
			select {
			case <-subscription.notify:
				continue
			case <-subscription.done:
				return
			}
		}
		event := subscription.pending[0]
		subscription.pending[0] = Event{}
		subscription.pending = subscription.pending[1:]
		closed := subscription.closed
		drain := subscription.drain
		subscription.mu.Unlock()
		if closed && !drain {
			return
		}
		select {
		case subscription.ch <- event:
		case <-subscription.done:
			return
		}
	}
}

func (subscription *runSubscription) Events() <-chan Event { return subscription.ch }

func (subscription *runSubscription) Next() (Event, bool) {
	event, ok := <-subscription.ch
	return event, ok
}

func (subscription *runSubscription) Close() {
	if subscription.manager == nil {
		return
	}
	subscription.manager.mu.Lock()
	subscription.manager.removeSubscriptionLocked(subscription, false)
	subscription.manager.mu.Unlock()
}
