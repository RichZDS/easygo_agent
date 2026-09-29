// Package workshop runs operator-defined CLI workflows in durable task workspaces.
// It is independent of the model gateway: each CLI owns its internal agent loop.
package workshop

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound = errors.New("task not found")
	ErrWorkflow = errors.New("unknown workflow")
	ErrFull     = errors.New("workshop capacity reached")
	ErrConflict = errors.New("task state or idempotency conflict")
	ErrRunLimit = fmt.Errorf("%w: run_limit", ErrConflict)
	ErrClosed   = errors.New("workshop closed")
	ErrInvalid  = errors.New("invalid request")
)

type Workflow struct {
	Runtime         string          `json:"runtime,omitempty"`
	AllowedRuntimes []string        `json:"allowed_runtimes,omitempty"`
	RuntimeSpec     *RuntimeProfile `json:"runtime_spec,omitempty"` // immutable operator snapshot, no secret values
	Name            string          `json:"name"`
	Version         string          `json:"version"`
	Instructions    string          `json:"instructions"`
	Engine          string          `json:"engine"` // claude or codex
	Model           string          `json:"model"`
	Policy          string          `json:"policy"` // read-only or workspace-write; required
	TimeoutSeconds  int             `json:"timeout_seconds"`
	Artifacts       []string        `json:"artifacts,omitempty"` // explicit relative regular-file paths
}

// WorkflowMetadata describes a discoverable workflow without its instructions
// or operator execution configuration.
type WorkflowMetadata struct {
	Runtime        string          `json:"runtime,omitempty"`
	Runtimes       []RuntimeChoice `json:"runtimes,omitempty"`
	Name           string          `json:"name"`
	Version        string          `json:"version"`
	Engine         string          `json:"engine"`
	Model          string          `json:"model"`
	Policy         string          `json:"policy"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	Artifacts      []string        `json:"artifacts"`
}

type EngineConfig struct {
	Binary       string   `json:"binary"` // trusted operator configuration only
	EnvAllowlist []string `json:"env_allowlist,omitempty"`
}

type Config struct {
	PackDir         string                    `json:"pack_dir,omitempty"`
	Sandbox         SandboxConfig             `json:"sandbox,omitempty"`
	RuntimeProfiles map[string]RuntimeProfile `json:"runtime_profiles,omitempty"`
	ModelGateway    *ModelGateway             `json:"model_gateway,omitempty"`
	Root            string                    `json:"root"`
	Concurrency     int                       `json:"concurrency"`
	QueueCapacity   int                       `json:"queue_capacity"`
	Workflows       []Workflow                `json:"workflows"`
	Engines         map[string]EngineConfig   `json:"engines"`
	MaxOutputBytes  int                       `json:"max_output_bytes,omitempty"`
	BearerTokenEnv  string                    `json:"bearer_token_env,omitempty"`
}

type SubmitRequest struct {
	Runtime        string `json:"runtime,omitempty"`
	Namespace      string `json:"namespace"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Workflow       string `json:"workflow"`
	Input          string `json:"input"`
}

type Status string

const (
	Queued      Status = "queued"
	Running     Status = "running"
	Cancelling  Status = "cancelling"
	Succeeded   Status = "succeeded"
	Failed      Status = "failed"
	Cancelled   Status = "cancelled"
	TimedOut    Status = "timed_out"
	Interrupted Status = "interrupted"
)

func terminal(s Status) bool { return s != Queued && s != Running && s != Cancelling }

type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty"`
}

type Artifact struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Run struct {
	Outcome         string     `json:"outcome,omitempty"`
	ID              string     `json:"id"`
	Input           string     `json:"input"`
	ResumeSessionID string     `json:"resume_session_id,omitempty"`
	SessionID       string     `json:"session_id,omitempty"`
	Status          Status     `json:"status"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	Text            string     `json:"text,omitempty"`
	Usage           Usage      `json:"usage"`
	Error           string     `json:"error,omitempty"`
	Artifacts       []Artifact `json:"artifacts,omitempty"`
}

type Task struct {
	ID             string    `json:"id"`
	Namespace      string    `json:"namespace"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	Workflow       Workflow  `json:"workflow"` // immutable snapshot
	Input          string    `json:"input"`
	Workspace      string    `json:"workspace"`
	Status         Status    `json:"status"`
	SessionID      string    `json:"session_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Runs           []Run     `json:"runs"`
}

type Event struct {
	Message   *CrewMessage `json:"message,omitempty"`
	Read      *CrewRead    `json:"read,omitempty"`
	Sequence  uint64       `json:"sequence"`
	RunID     string       `json:"run_id"`
	Time      time.Time    `json:"time"`
	Kind      string       `json:"kind"`
	Text      string       `json:"text,omitempty"`
	SessionID string       `json:"session_id,omitempty"`
	Usage     *Usage       `json:"usage,omitempty"`
}

type Invocation struct {
	Crew               CrewChannel
	WorkerInstructions string
	Namespace          string
	Workflow           Workflow
	Workspace          string
	Input              string
	SessionID          string
}

type Result struct {
	SessionID string
	Text      string
	Usage     Usage
}

// Runner must return only after its subprocesses stop, honor ctx, and propagate
// emit errors. Successful return means an engine terminal success was observed.
type Runner interface {
	Run(ctx context.Context, in Invocation, emit func(Event) error) (Result, error)
}
