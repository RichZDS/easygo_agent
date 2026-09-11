package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"easygo-agent/internal/conversation"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultQueueWorkers  = 4
	defaultQueuePoll     = 250 * time.Millisecond
	defaultQueueLeaseTTL = 30 * time.Second
)

// QueueConfig controls the application-wide durable run scheduler.
type QueueConfig struct {
	MaxPending   int
	MaxWorkers   int
	PollInterval time.Duration
	LeaseTTL     time.Duration
}

func (config QueueConfig) withDefaults() QueueConfig {
	if config.MaxPending < 1 {
		config.MaxPending = conversation.DefaultMaxPendingRuns
	}
	if config.MaxWorkers < 1 {
		config.MaxWorkers = defaultQueueWorkers
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaultQueuePoll
	}
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = defaultQueueLeaseTTL
	}
	return config
}

func DefaultQueueConfig() QueueConfig {
	return QueueConfig{MaxPending: conversation.DefaultMaxPendingRuns, MaxWorkers: defaultQueueWorkers, PollInterval: defaultQueuePoll, LeaseTTL: defaultQueueLeaseTTL}
}

// QueueManager is the shared seam used by CLI, TUI and HTTP modes.
type QueueManager interface {
	Submit(context.Context, string, string, string, string) (conversation.RunRecord, RunHandle, error)
	Get(context.Context, string, string, string) (conversation.RunRecord, error)
	List(context.Context, string, string, int) ([]conversation.RunRecord, error)
	Cancel(context.Context, string, string, string) (conversation.RunRecord, error)
	Subscribe(context.Context, string, string, string) (Subscription, error)
	Close() error
}

// RunHandle identifies an accepted request. Closing a handle only releases
// the caller's handle; it never cancels the durable run.
type RunHandle interface {
	Run() conversation.RunRecord
	Cancel()
	Close()
}

// Subscription observes one run. Publication is decoupled from the worker so
// a slow observer never blocks execution or causes an event to be dropped.
type Subscription interface {
	Events() <-chan Event
	Close()
}

type queueManager struct {
	store        conversation.QueueStore
	memory       conversation.MemoryStore
	agent        adk.TypedAgent[*schema.AgenticMessage]
	cfg          QueueConfig
	ctx          context.Context
	cancel       context.CancelFunc
	workerCtx    context.Context
	workerCancel context.CancelFunc
	wake         chan struct{}
	wg           sync.WaitGroup

	mu          sync.Mutex
	closed      bool
	active      map[string]*activeRun
	subscribers map[subscriptionKey]map[*runSubscription]struct{}
}

// Manager is the concrete scheduler returned by NewQueueManager.
type Manager = queueManager

type activeRun struct {
	sessionID string
	record    conversation.RunRecord
	lease     conversation.RunLease
	run       Run
}

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

type queueRunHandle struct {
	manager *queueManager
	user    string
	session string
	mu      sync.Mutex
	record  conversation.RunRecord
}

// NewQueueManager starts the shared scheduler and immediately recovers durable
// runs left by an expired worker lease.
func NewQueueManager(parent context.Context, store conversation.QueueStore, agent adk.TypedAgent[*schema.AgenticMessage], config QueueConfig, memory ...conversation.MemoryStore) *Manager {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	workerCtx, workerCancel := context.WithCancel(ctx)
	config = config.withDefaults()
	manager := &queueManager{
		store:        store,
		agent:        agent,
		cfg:          config,
		ctx:          ctx,
		cancel:       cancel,
		workerCtx:    workerCtx,
		workerCancel: workerCancel,
		wake:         make(chan struct{}, 1),
		active:       make(map[string]*activeRun),
		subscribers:  make(map[subscriptionKey]map[*runSubscription]struct{}),
	}
	if len(memory) > 0 {
		manager.memory = memory[0]
	}
	if configurable, ok := store.(interface{ SetMaxPendingRuns(int) }); ok {
		configurable.SetMaxPendingRuns(config.MaxPending)
	}
	if store == nil {
		return manager
	}
	_ = store.RecoverExpired(context.Background(), time.Now().UTC())
	manager.wg.Add(1)
	go manager.recoverExpired()
	for i := 0; i < config.MaxWorkers; i++ {
		manager.wg.Add(1)
		go manager.worker(i + 1)
	}
	return manager
}

// NewManager is a concise compatibility alias for NewQueueManager.
func NewManager(parent context.Context, store conversation.QueueStore, agent adk.TypedAgent[*schema.AgenticMessage], config QueueConfig) *Manager {
	return NewQueueManager(parent, store, agent, config)
}

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
			}
		}
	}
}

func (manager *queueManager) Submit(ctx context.Context, user, sessionID, input, idempotencyKey string) (conversation.RunRecord, RunHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return conversation.RunRecord{}, nil, ErrQueueClosed
	}
	if manager.ctx.Err() != nil {
		manager.mu.Unlock()
		return conversation.RunRecord{}, nil, ErrQueueClosed
	}
	if manager.store == nil {
		manager.mu.Unlock()
		return conversation.RunRecord{}, nil, ErrStoreUnavailable
	}
	if err := ctx.Err(); err != nil {
		manager.mu.Unlock()
		return conversation.RunRecord{}, nil, err
	}
	// Keep Close from racing an accepted enqueue. Once the row is written,
	// releasing this lock lets Close preserve it as queued if no worker remains.
	// The enqueue itself still belongs to the caller's request: if persistence
	// has not accepted the row yet, cancellation should stop the operation.
	// Once this call returns successfully, workers use manager.ctx and therefore
	// no longer inherit the POST request's cancellation.
	record, err := manager.store.Enqueue(ctx, user, sessionID, input, idempotencyKey)
	if err != nil {
		manager.mu.Unlock()
		return conversation.RunRecord{}, nil, wrapStoreError(err)
	}
	handle := &queueRunHandle{manager: manager, user: user, session: sessionID, record: record}
	// A worker may claim the row concurrently, but it cannot publish its
	// running state until it acquires manager.mu. Publish the accepted state
	// before releasing that lock so observers always see queued first.
	manager.publishLocked(eventForRecord(record))
	manager.mu.Unlock()
	manager.signal()
	return record, handle, nil
}

func (manager *queueManager) Get(ctx context.Context, user, sessionID, runID string) (conversation.RunRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	closed := manager.closed
	manager.mu.Unlock()
	if closed || manager.ctx.Err() != nil {
		return conversation.RunRecord{}, ErrQueueClosed
	}
	if manager.store == nil {
		return conversation.RunRecord{}, ErrStoreUnavailable
	}
	record, err := manager.store.GetRun(ctx, user, sessionID, runID)
	return record, wrapStoreError(err)
}

func (manager *queueManager) List(ctx context.Context, user, sessionID string, limit int) ([]conversation.RunRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	closed := manager.closed
	manager.mu.Unlock()
	if closed || manager.ctx.Err() != nil {
		return nil, ErrQueueClosed
	}
	if manager.store == nil {
		return nil, ErrStoreUnavailable
	}
	runs, err := manager.store.ListRuns(ctx, user, sessionID, limit)
	return runs, wrapStoreError(err)
}

func (manager *queueManager) Cancel(ctx context.Context, user, sessionID, runID string) (conversation.RunRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	closed := manager.closed
	manager.mu.Unlock()
	if closed || manager.ctx.Err() != nil {
		return conversation.RunRecord{}, ErrQueueClosed
	}
	if manager.store == nil {
		return conversation.RunRecord{}, ErrStoreUnavailable
	}
	record, err := manager.store.RequestCancel(ctx, user, sessionID, runID)
	if err != nil {
		return conversation.RunRecord{}, wrapStoreError(err)
	}
	manager.mu.Lock()
	active := manager.active[runID]
	manager.mu.Unlock()
	if active != nil && record.Status == conversation.RunRunning {
		active.run.Cancel()
	} else if record.Status == conversation.RunCanceled {
		manager.publish(eventForRecord(record))
	}
	manager.signal()
	return record, nil
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
	if manager.subscribers[key] == nil {
		manager.subscribers[key] = make(map[*runSubscription]struct{})
	}
	manager.subscribers[key][subscription] = struct{}{}
	// Read current state while registered. Events produced after this point are
	// queued behind the initial snapshot and cannot be missed.
	record, err := manager.store.GetRun(ctx, user, sessionID, runID)
	if err != nil {
		delete(manager.subscribers[key], subscription)
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

func (manager *queueManager) Close() error {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closed = true
	active := make([]*activeRun, 0, len(manager.active))
	for _, run := range manager.active {
		active = append(active, run)
	}
	manager.mu.Unlock()
	// Cancel active runtimes before stopping workers. Their runtime finalizer
	// persists the canceled terminal state while the Store is still open.
	for _, run := range active {
		run.run.Cancel()
	}
	if manager.workerCancel != nil {
		manager.workerCancel()
	}
	manager.signal()
	manager.wg.Wait()
	manager.cancel()
	manager.mu.Lock()
	for key, subscriptions := range manager.subscribers {
		for subscription := range subscriptions {
			manager.removeSubscriptionLocked(subscription, false)
		}
		delete(manager.subscribers, key)
	}
	manager.mu.Unlock()
	return nil
}

func (manager *queueManager) worker(number int) {
	defer manager.wg.Done()
	workerID := "worker-" + formatWorkerNumber(number)
	for {
		if manager.workerContext().Err() != nil {
			return
		}
		lease, err := manager.store.ClaimNext(manager.workerContext(), workerID, manager.cfg.LeaseTTL)
		if err == nil {
			manager.execute(workerID, lease)
			continue
		}
		if !errors.Is(err, conversation.ErrNoQueuedRun) && manager.workerContext().Err() != nil {
			return
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

func formatWorkerNumber(number int) string {
	return strconv.Itoa(number)
}

func (manager *queueManager) execute(workerID string, lease conversation.RunLease) {
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
	_ = workerID
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
				run.Cancel()
				return
			}
			checkCtx, checkCancel := context.WithTimeout(context.Background(), interval)
			requested, err := lease.CancelRequested(checkCtx)
			checkCancel()
			if err != nil || requested {
				run.Cancel()
				return
			}
		}
	}
}

func (manager *queueManager) signal() {
	select {
	case manager.wake <- struct{}{}:
	default:
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
	for key, subscriptions := range manager.subscribers {
		if key.runID != event.RunID {
			continue
		}
		for subscription := range subscriptions {
			subscription.enqueue(event)
			if event.IsTerminal() {
				// Let dispatch drain the terminal event and remove itself. Keeping
				// it in the registry gives Close a way to interrupt a blocked
				// observer.
				subscription.shutdown(true)
			}
		}
	}
}

func (manager *queueManager) subscriptionFinished(subscription *runSubscription) {
	if manager == nil || subscription == nil {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if subscriptions := manager.subscribers[subscription.key]; subscriptions != nil {
		delete(subscriptions, subscription)
		if len(subscriptions) == 0 {
			delete(manager.subscribers, subscription.key)
		}
	}
}

func (manager *queueManager) removeSubscriptionLocked(subscription *runSubscription, drain bool) {
	delete(manager.subscribers[subscription.key], subscription)
	if len(manager.subscribers[subscription.key]) == 0 {
		delete(manager.subscribers, subscription.key)
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

func (handle *queueRunHandle) Run() conversation.RunRecord {
	if handle.manager == nil {
		handle.mu.Lock()
		defer handle.mu.Unlock()
		return conversation.CloneRunRecord(handle.record)
	}
	handle.mu.Lock()
	runID := handle.record.ID
	handle.mu.Unlock()
	record, err := handle.manager.Get(context.Background(), handle.user, handle.session, runID)
	if err != nil {
		handle.mu.Lock()
		defer handle.mu.Unlock()
		return conversation.CloneRunRecord(handle.record)
	}
	handle.mu.Lock()
	handle.record = record
	result := conversation.CloneRunRecord(handle.record)
	handle.mu.Unlock()
	return result
}

func (handle *queueRunHandle) ID() string {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.record.ID
}

func (handle *queueRunHandle) Cancel() {
	if handle.manager != nil {
		_, _ = handle.manager.Cancel(context.Background(), handle.user, handle.session, handle.ID())
	}
}

func (handle *queueRunHandle) Close() {}

func eventForRecord(record conversation.RunRecord) Event {
	kind := EventRunning
	switch record.Status {
	case conversation.RunQueued:
		kind = EventQueued
	case conversation.RunCompleted:
		kind = EventCompleted
	case conversation.RunCanceled:
		kind = EventCanceled
	case conversation.RunFailed:
		kind = EventFailed
	}
	event := Event{Kind: kind, RunID: record.ID, Status: record.Status, Position: record.Position, Text: record.ResultText}
	if record.Error != "" {
		event.Err = errors.New(record.Error)
	}
	return event
}

func isRunTerminal(status conversation.RunStatus) bool {
	return status == conversation.RunCompleted || status == conversation.RunFailed || status == conversation.RunCanceled
}

func wrapStoreError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{
		conversation.ErrNotFound,
		conversation.ErrEmptyInput,
		conversation.ErrQueueFull,
		conversation.ErrIdempotencyConflict,
		conversation.ErrBusy,
		conversation.ErrInvalidUser,
		conversation.ErrNoQueuedRun,
		conversation.ErrCancellationRequested,
		context.Canceled,
		context.DeadlineExceeded,
	} {
		if errors.Is(err, known) {
			return err
		}
	}
	return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
}

var _ QueueManager = (*queueManager)(nil)
var _ RunHandle = (*queueRunHandle)(nil)
var _ Subscription = (*runSubscription)(nil)
