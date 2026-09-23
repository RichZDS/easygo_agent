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
	Source          string     `json:"source,omitempty"`
	NotificationID  string     `json:"notification_id,omitempty"`
	ID              string     `json:"run_id"`
	SessionID       string     `json:"session_id"`
	Username        string     `json:"-"`
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

// MemoryKind controls both recall semantics and the on-disk profile document
// written for a user. The first four kinds map to the requested files; the
// remaining kinds retain durable preferences without forcing an inaccurate
// bucket.
type MemoryKind string

const (
	MemoryKindAgent      MemoryKind = "agent"
	MemoryKindMemory     MemoryKind = "memory"
	MemoryKindExperiment MemoryKind = "experiment"
	MemoryKindError      MemoryKind = "error"
	MemoryKindPreference MemoryKind = "preference"
	MemoryKindStyle      MemoryKind = "style"
	MemoryKindPrompt     MemoryKind = "prompt"
	MemoryKindConstraint MemoryKind = "constraint"
)

func (k MemoryKind) Valid() bool {
	switch k {
	case MemoryKindAgent, MemoryKindMemory, MemoryKindExperiment, MemoryKindError,
		MemoryKindPreference, MemoryKindStyle, MemoryKindPrompt, MemoryKindConstraint:
		return true
	default:
		return false
	}
}

// LongTermMemory is one active or archived user-memory record. The time,
// confidence, provenance, version and access fields make a memory auditable
// and allow retrieval to balance freshness with demonstrated usefulness.
type LongTermMemory struct {
	ID             string     `json:"id"`
	Username       string     `json:"username"`
	Kind           MemoryKind `json:"kind"`
	Content        string     `json:"content"`
	Tags           []string   `json:"tags,omitempty"`
	Importance     float64    `json:"importance"`
	Confidence     float64    `json:"confidence"`
	SourceSessions []string   `json:"source_sessions,omitempty"`
	SourceTurnIDs  []int64    `json:"source_turn_ids,omitempty"`
	CallCount      int64      `json:"call_count"`
	FirstSeenAt    time.Time  `json:"first_seen_at"`
	LastSeenAt     time.Time  `json:"last_seen_at"`
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ArchivedAt     *time.Time `json:"archived_at,omitempty"`
	ProfileSlot    int        `json:"profile_slot,omitempty"`
	Version        int        `json:"version"`
	State          string     `json:"state"`
	SupersededBy   *string    `json:"superseded_by,omitempty"`
	Score          float64    `json:"score,omitempty"`
}

// MemoryDraft is the model-reviewed representation used to reconcile the five
// active profile rows. ID may point only to an existing record for this user;
// omitting it creates a new versioned record.
type MemoryDraft struct {
	ID             string
	Kind           MemoryKind
	Content        string
	Tags           []string
	Importance     float64
	Confidence     float64
	SourceSessions []string
	SourceTurnIDs  []int64
	ExpiresAt      *time.Time
}

// TranscriptTurn is raw, auditable conversation data supplied to the memory
// agent in bounded batches. It keeps native messages, so tool results and
// corrections are not silently discarded during extraction.
type TranscriptTurn struct {
	SessionID string
	Turn
}

type Store interface {
	Create(context.Context, string) (Session, error)
	List(context.Context, string, int, int) ([]Session, error)
	History(context.Context, string, string, int64, int) ([]Turn, error)
	Begin(context.Context, string, string) (Lease, error)
	TranscriptStore
	Close()
}

// MemoryStore is the durable, user-scoped long-term-memory boundary. It is
// intentionally separate from a conversation lease: recalling a profile must
// never hold a conversation lock while an LLM is thinking.
type MemoryStore interface {
	Recall(context.Context, string, int) ([]LongTermMemory, error)
	ActiveProfile(context.Context, string) ([]LongTermMemory, error)
	ReplaceProfile(context.Context, string, []MemoryDraft) ([]LongTermMemory, error)
	History(context.Context, string, string) ([]LongTermMemory, error)
	TryAcquireMemoryJob(context.Context, string) (MemoryJobLease, bool, error)
	MemoryCheckpoint(context.Context, string) (time.Time, bool, error)
	SetMemoryCheckpoint(context.Context, string, time.Time) error
	Close()
}

// MemoryJobLease provides cross-process mutual exclusion for one user's daily
// consolidation. Gateway and TUI can therefore both host the scheduler
// without causing duplicate extraction or conflicting five-slot updates.
type MemoryJobLease interface{ Close() }

// TranscriptStore is the read-only view that the consolidation worker uses to
// consume completed native conversation turns from the conversation database.
// It is intentionally a different boundary from MemoryStore so production can
// give the worker read-only conversation credentials and separate memory DSN.
type TranscriptStore interface {
	Transcript(context.Context, string, time.Time, time.Time) ([]TranscriptTurn, error)
	UsersWithTranscript(context.Context, time.Time, time.Time) ([]string, error)
}

// RunPhase is one durable execution phase. It carries correlation and outcome
// only: no prompt, tool arguments, tool result, or message body.
type RunPhase struct {
	RunID        string    `json:"run_id"`
	ExecutionID  string    `json:"execution_id"`
	Sequence     int64     `json:"sequence"`
	SpanID       int64     `json:"span_id"`
	ParentSpanID int64     `json:"parent_span_id"`
	Phase        string    `json:"phase"`
	Name         string    `json:"name"`
	Event        string    `json:"event"`
	Status       string    `json:"status,omitempty"`
	DurationMS   *float64  `json:"duration_ms,omitempty"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// RunPhasePage is one stable page of a run's phases. NextAfter is the sequence
// cursor: the following call passes it as afterSequence.
type RunPhasePage struct {
	Phases    []RunPhase `json:"phases"`
	NextAfter int64      `json:"next_after"`
}

// PhaseStore is the durable phase log shared by Memory and PostgreSQL. A reader
// in another process uses this seam and never the execution that emitted spans.
type PhaseStore interface {
	AppendRunPhase(context.Context, RunPhase) error
	ListRunPhases(context.Context, string, string, string, int64) (RunPhasePage, error)
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

// NotificationStore accepts trusted internal events separately from user input.
type NotificationStore interface {
	EnqueueInternal(context.Context, string, string, string, string) (RunRecord, error)
}

func RunInput(record RunRecord) *schema.AgenticMessage {
	if record.Source == "task_notification" {
		return schema.SystemAgenticMessage("Internal background task notification. Summarize this evidence under the latest conversation constraints. This is not a user request; do not launch or resume delegation. Task data:\n" + record.Input)
	}
	return schema.UserAgenticMessage(record.Input)
}
func auditRun(record RunRecord, outputs []*schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
	return Clone(append([]*schema.AgenticMessage{RunInput(record)}, outputs...))
}

type NotificationReader interface {
	NotificationRuns(context.Context, string, string, int64) ([]RunRecord, error)
}
