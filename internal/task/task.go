// Package task runs durable, isolated background agents independently of session leases.
package task

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"easygo-agent/internal/toolregistry"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type Status string

const (
	Queued        Status = "queued"
	Running       Status = "running"
	Blocked       Status = "blocked"
	Completed     Status = "completed"
	Failed        Status = "failed"
	Canceled      Status = "canceled"
	FormatVersion        = 1
)

var (
	ErrNotFound  = errors.New("task not found")
	ErrBusy      = errors.New("task is queued or running")
	ErrLeaseLost = errors.New("task lease lost")
	ErrNoTask    = errors.New("no queued task")
	ErrInvalid   = errors.New("invalid task request")
	ErrConflict  = errors.New("task idempotency key belongs to another brief")
)

type Owner struct {
	User    string `json:"user"`
	Session string `json:"session"`
	Run     string `json:"run"`
}
type Brief struct {
	Role        string   `json:"role" jsonschema:"required"`
	Goal        string   `json:"goal" jsonschema:"required"`
	Constraints []string `json:"constraints"`
	Acceptance  []string `json:"acceptance" jsonschema:"required"`
	Materials   []string `json:"materials"`
	Key         string   `json:"idempotency_key" jsonschema:"required,description=Stable key for this delegation within the initiating run"`
}
type PlanStep struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}
type Result struct {
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
	Unmet    []string `json:"unmet,omitempty"`
}
type Call struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Arguments string             `json:"arguments"`
	Key       string             `json:"idempotency_key"`
	Retry     toolregistry.Retry `json:"retry"`
	Started   bool               `json:"started"`
	Done      bool               `json:"done"`
	Approved  bool               `json:"retry_approved,omitempty"`
	Result    string             `json:"result"`
	Error     string             `json:"error,omitempty"`
}
type Checkpoint struct {
	StepLimit int                      `json:"step_limit"`
	Continue  bool                     `json:"continue_requested,omitempty"`
	Format    int                      `json:"format"`
	Version   string                   `json:"configuration_version"`
	Messages  []*schema.AgenticMessage `json:"messages"`
	Calls     []Call                   `json:"pending_calls"`
	Steps     int                      `json:"steps"`
	Deadline  time.Time                `json:"deadline"`
}
type Event struct {
	Seq     int64     `json:"seq"`
	Version int       `json:"version"`
	Kind    string    `json:"kind"`
	Detail  string    `json:"detail"`
	At      time.Time `json:"at"`
}
type Execution struct {
	ParentRun  string     `json:"parent_run,omitempty"`
	Version    int        `json:"version"`
	Checkpoint Checkpoint `json:"checkpoint"`
	Status     Status     `json:"status"`
	Result     Result     `json:"result"`
	Reason     string     `json:"reason"`
}
type Task struct {
	RequestFingerprint string      `json:"request_fingerprint"`
	ParentRun          string      `json:"execution_parent_run,omitempty"`
	ID                 string      `json:"task_id"`
	Owner              Owner       `json:"owner"`
	Brief              Brief       `json:"brief"`
	Status             Status      `json:"status"`
	Version            int         `json:"version"`
	Plan               []PlanStep  `json:"plan"`
	Progress           string      `json:"progress"`
	Result             Result      `json:"result"`
	Reason             string      `json:"reason,omitempty"`
	Calls              []Call      `json:"calls"`
	Checkpoint         Checkpoint  `json:"checkpoint"`
	History            []Execution `json:"history,omitempty"`
	Events             []Event     `json:"events"`
	CreatedAt          time.Time   `json:"created_at"`
	UpdatedAt          time.Time   `json:"updated_at"`
	Token              string      `json:"-"`
	LeaseUntil         time.Time   `json:"-"`
}

func (t *Task) Event(kind, detail string) {
	t.UpdatedAt = time.Now().UTC()
	t.Events = append(t.Events, Event{Seq: int64(len(t.Events) + 1), Version: t.Version, Kind: kind, Detail: detail, At: t.UpdatedAt})
}
func (t Task) NotificationID() string { return fmt.Sprintf("task:%s:%d", t.ID, t.Version) }
func (t Task) Notification() string {
	data, _ := json.Marshal(struct {
		ID      string `json:"task_id"`
		Version int    `json:"version"`
		Brief   Brief  `json:"brief"`
		Status  Status `json:"status"`
		Result  Result `json:"result"`
		Reason  string `json:"reason"`
	}{t.ID, t.Version, t.Brief, t.Status, t.Result, t.Reason})
	return string(data)
}
func terminal(s Status) bool { return s == Completed || s == Failed || s == Blocked || s == Canceled }
func newTask(owner Owner, brief Brief) (Task, error) {
	if owner.User == "" || owner.Session == "" || owner.Run == "" || strings.TrimSpace(brief.Goal) == "" || len(brief.Acceptance) == 0 || strings.TrimSpace(brief.Key) == "" {
		return Task{}, ErrInvalid
	}
	now := time.Now().UTC()
	t := Task{RequestFingerprint: briefFingerprint(brief), ParentRun: owner.Run, ID: uuid.NewString(), Owner: owner, Brief: brief, Status: Queued, Version: 1, CreatedAt: now, UpdatedAt: now}
	t.Event("queued", brief.Goal)
	return t, nil
}
func clone(t Task) Task {
	b, _ := json.Marshal(t)
	var out Task
	_ = json.Unmarshal(b, &out)
	out.Token = t.Token
	out.LeaseUntil = t.LeaseUntil
	return out
}
func briefFingerprint(brief Brief) string {
	data, _ := json.Marshal(brief)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

type Decision struct {
	Action string  `json:"action"`
	Result *string `json:"result,omitempty"`
}
type Resume struct {
	ParentRun    string              `json:"-"`
	Instructions string              `json:"instructions"`
	Decisions    map[string]Decision `json:"decisions"`
}

func resume(t *Task, in Resume) error {
	if t.Status == Running || t.Status == Queued {
		return ErrBusy
	}
	previous := clone(*t)
	for id := range in.Decisions {
		found := false
		for _, c := range t.Checkpoint.Calls {
			if c.ID == id && !c.Done {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%w: unknown unresolved call %s", ErrInvalid, id)
		}
	}
	for i := range t.Checkpoint.Calls {
		c := &t.Checkpoint.Calls[i]
		if c.Done {
			continue
		}
		d, ok := in.Decisions[c.ID]
		if !ok {
			if c.Started && c.Retry != toolregistry.ReadOnly && c.Retry != toolregistry.Idempotent {
				return fmt.Errorf("%w: call %s requires retry or verified result", ErrInvalid, c.ID)
			}
			continue
		}
		switch d.Action {
		case "retry":
			c.Approved = true
		case "result":
			if d.Result == nil {
				return fmt.Errorf("%w: verified result required", ErrInvalid)
			}
			c.Result = *d.Result
			c.Error = ""
			c.Done = true
		default:
			return fmt.Errorf("%w: action must be retry or result", ErrInvalid)
		}
	}
	t.History = append(t.History, Execution{ParentRun: previous.ParentRun, Version: t.Version, Checkpoint: previous.Checkpoint, Status: previous.Status, Result: previous.Result, Reason: previous.Reason})
	t.ParentRun = in.ParentRun
	t.Version++
	t.Status = Queued
	t.Reason = ""
	t.Result = Result{}
	t.Token = ""
	t.LeaseUntil = time.Time{}
	t.Checkpoint.Steps = 0
	t.Checkpoint.StepLimit = 0
	t.Checkpoint.Continue = strings.TrimSpace(in.Instructions) != ""
	t.Checkpoint.Deadline = time.Time{}
	// Instructions are appended only after pending tool results are paired.
	if strings.TrimSpace(in.Instructions) != "" {
		t.Brief.Constraints = append(t.Brief.Constraints, in.Instructions)
	}
	t.Event("resumed", in.Instructions)
	return nil
}

// Store mutations must fence stale workers and atomically record terminal outbox entries.
type Store interface {
	Create(context.Context, Owner, Brief) (Task, error)
	Get(context.Context, Owner, string) (Task, error)
	List(context.Context, Owner) ([]Task, error)
	Claim(context.Context, time.Duration) (Task, error)
	Save(context.Context, Task, string) error
	Heartbeat(context.Context, string, string, time.Duration) error
	Release(context.Context, string, string) error
	AppendEvent(context.Context, string, string, string, string) error
	Cancel(context.Context, Owner, string) (Task, error)
	Resume(context.Context, Owner, string, Resume) (Task, error)
	UpdatePlan(context.Context, Owner, string, []PlanStep) (Task, error)
	Pending(context.Context) ([]Notification, error)
	MarkEnqueued(context.Context, string) error
}
type Notification struct {
	ID      string
	Owner   Owner
	Content string
}

func authorized(t Task, o Owner) bool { return t.Owner.User == o.User && t.Owner.Session == o.Session }
