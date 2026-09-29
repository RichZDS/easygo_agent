package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"easygo-agent/internal/agent/telemetry"
	"easygo-agent/internal/clientapi"
	"easygo-agent/internal/conversation"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	defaultQueueWorkers  = 4
	defaultQueuePoll     = 250 * time.Millisecond
	defaultQueueLeaseTTL = 30 * time.Second
)

// QueueConfig controls the application-wide durable run scheduler.
type TaskHooks interface {
	Summary(context.Context, string, string) (string, error)
	CancelChildren(context.Context, string, string, string) error
}
type taskSummaryKey struct{}
type QueueConfig struct {
	Tasks        TaskHooks
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
type QueueManager = clientapi.QueueManager

// RunHandle identifies an accepted request. Closing a handle only releases
// the caller's handle; it never cancels the durable run.
type RunHandle = clientapi.RunHandle

// Subscription observes one run. Publication is decoupled from the worker so
// a slow observer never blocks execution or causes an event to be dropped.
type Subscription = clientapi.Subscription

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
	subscribers map[string]map[*runSubscription]struct{}
}

// Manager is the concrete scheduler returned by NewQueueManager.
type Manager = queueManager

type activeRun struct {
	sessionID string
	record    conversation.RunRecord
	lease     conversation.RunLease
	run       Run
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
		subscribers:  make(map[string]map[*runSubscription]struct{}),
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
	if err := store.RecoverExpired(ctx, time.Now().UTC()); err != nil {
		telemetry.Logger(ctx).Error("queue initial recovery failed", zap.Error(err))
	}
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
	telemetry.Logger(ctx).Info("queue accepted", zap.String("run_id", record.ID), zap.String("session_id", record.SessionID), zap.String("status", string(record.Status)), zap.Int("position", record.Position))
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
	if manager.cfg.Tasks != nil && (record.CancelRequested || record.Status == conversation.RunCanceled) {
		if err = manager.cfg.Tasks.CancelChildren(ctx, user, sessionID, runID); err != nil {
			return record, err
		}
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

func (manager *queueManager) signal() {
	select {
	case manager.wake <- struct{}{}:
	default:
	}
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
