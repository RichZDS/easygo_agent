// Package conversation stores Eino messages without a parallel chat schema.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

var (
	ErrNotFound              = errors.New("conversation not found")
	ErrBusy                  = errors.New("conversation already running")
	ErrInvalidUser           = errors.New("username must contain 1 to 128 characters")
	ErrEmptyInput            = errors.New("agent input cannot be empty")
	ErrNoQueuedRun           = errors.New("no queued run")
	ErrQueueFull             = errors.New("conversation queue is full")
	ErrIdempotencyConflict   = errors.New("idempotency key already belongs to a different input")
	ErrLeaseLost             = errors.New("run lease is no longer valid")
	ErrCancellationRequested = errors.New("run cancellation requested")
	ErrInvalidRunStatus      = errors.New("invalid run status")
)

// RunStatus is the durable lifecycle state of a submitted user request.
type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

const DefaultMaxPendingRuns = 100

// RunRecord is the queue-facing representation of one submitted request.
// Position is computed for queued records and is zero for other states.
type RunRecord struct {
	ID              string     `json:"run_id"`
	SessionID       string     `json:"session_id"`
	Input           string     `json:"input"`
	Status          RunStatus  `json:"status"`
	Position        int        `json:"position"`
	IdempotencyKey  string     `json:"-"`
	CancelRequested bool       `json:"cancel_requested,omitempty"`
	ResultText      string     `json:"result_text,omitempty"`
	Error           string     `json:"error,omitempty"`
	TurnID          int64      `json:"turn_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	WorkerID        string     `json:"-"`
	LeaseExpiresAt  time.Time  `json:"-"`
	ClaimToken      string     `json:"-"`
}

// CloneRunRecord returns a value that does not alias the timestamp pointers
// held by a Memory store or a lease. Queue callers are free to retain and
// inspect records without being able to mutate store state accidentally.
func CloneRunRecord(record RunRecord) RunRecord {
	if record.StartedAt != nil {
		started := *record.StartedAt
		record.StartedAt = &started
	}
	if record.FinishedAt != nil {
		finished := *record.FinishedAt
		record.FinishedAt = &finished
	}
	return record
}

type Session struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Turn is an audit record. Messages are the complete native outputs received,
// including on failed/canceled runs. Context is committed separately.
type Turn struct {
	ID        int64                    `json:"id"`
	Status    string                   `json:"status"`
	Messages  []*schema.AgenticMessage `json:"messages"`
	CreatedAt time.Time                `json:"created_at"`
}

type Store interface {
	Create(context.Context, string) (Session, error)
	List(context.Context, string, int, int) ([]Session, error)
	History(context.Context, string, string, int64, int) ([]Turn, error)
	Begin(context.Context, string, string) (Lease, error)
	Close()
}

// QueueStore extends Store with durable submission and worker-claim semantics.
// It is a real seam because Memory and PostgreSQL both satisfy it.
type QueueStore interface {
	Store
	Enqueue(context.Context, string, string, string, string) (RunRecord, error)
	GetRun(context.Context, string, string, string) (RunRecord, error)
	ListRuns(context.Context, string, string, int) ([]RunRecord, error)
	RequestCancel(context.Context, string, string, string) (RunRecord, error)
	ClaimNext(context.Context, string, time.Duration) (RunLease, error)
	RecoverExpired(context.Context, time.Time) error
}

// RunLease owns one claimed queued run until it is committed or released.
type RunLease interface {
	Run() RunRecord
	Messages() []*schema.AgenticMessage
	CommitRun(context.Context, []*schema.AgenticMessage, RunStatus, []*schema.AgenticMessage, string, string) error
	CancelRequested(context.Context) (bool, error)
	Heartbeat(context.Context, time.Duration) error
	Close()
}

func validRunStatus(status RunStatus) bool {
	switch status {
	case RunQueued, RunRunning, RunCompleted, RunFailed, RunCanceled:
		return true
	default:
		return false
	}
}

// Lease exclusively owns one conversation until Close. Commit atomically saves
// the next model context and one audit turn; callers must always close the lease.
type Lease interface {
	Messages() []*schema.AgenticMessage
	Commit(context.Context, []*schema.AgenticMessage, Turn) error
	Close()
}

// CommitRun adapts the legacy conversation lease to the run-oriented commit
// seam. Keeping audit construction here means runtime does not need to know
// how a Turn is represented, while existing Store/Lease users remain source
// compatible.
func CommitRun(ctx context.Context, lease Lease, next []*schema.AgenticMessage, status, input string, outputs []*schema.AgenticMessage) error {
	if lease == nil {
		return ErrNotFound
	}
	audit, err := runAudit(input, outputs)
	if err != nil {
		return err
	}
	return lease.Commit(ctx, next, Turn{Status: status, Messages: audit})
}

func ValidateUser(user string) error {
	if strings.TrimSpace(user) != user || user == "" || len([]rune(user)) > 128 {
		return ErrInvalidUser
	}
	return nil
}

func Clone(messages []*schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
	data, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	var result []*schema.AgenticMessage
	err = json.Unmarshal(data, &result)
	return result, err
}

func runAudit(input string, outputs []*schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
	messages := make([]*schema.AgenticMessage, 0, len(outputs)+1)
	messages = append(messages, schema.UserAgenticMessage(input))
	messages = append(messages, outputs...)
	return Clone(messages)
}

func pageSize(limit int) int {
	if limit < 1 || limit > 100 {
		return 50
	}
	return limit
}
