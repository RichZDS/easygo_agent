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
	ErrNotFound    = errors.New("conversation not found")
	ErrBusy        = errors.New("conversation already running")
	ErrInvalidUser = errors.New("username must contain 1 to 128 characters")
)

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
	TryAcquireMemoryJob(context.Context, string) (MemoryJobLease, bool, error)
	MemoryCheckpoint(context.Context, string) (time.Time, bool, error)
	SetMemoryCheckpoint(context.Context, string, time.Time) error
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

// Lease exclusively owns one conversation until Close. Commit atomically saves
// the next model context and one audit turn; callers must always close the lease.
type Lease interface {
	Messages() []*schema.AgenticMessage
	Commit(context.Context, []*schema.AgenticMessage, Turn) error
	Close()
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

func pageSize(limit int) int {
	if limit < 1 || limit > 100 {
		return 50
	}
	return limit
}
