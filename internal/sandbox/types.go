package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	StateApplied     = "applied"
	StateStarting    = "starting"
	StateActive      = "active"
	StateBusy        = "busy"
	StateWarmIdle    = "warm_idle"
	StateHibernating = "hibernating"
	StateHibernated  = "hibernated"
	StateDestroying  = "destroying"
	StateDestroyed   = "destroyed"
)

const (
	CodeInvalidRequest    = "invalid_request"
	CodeUnauthorized      = "unauthorized"
	CodeApplicationAbsent = "application_not_found"
	CodeApplicationBusy   = "application_busy"
	CodeCapacityExhausted = "capacity_exhausted"
	CodeApplicationLimit  = "application_limit_reached"
	CodeStoragePressure   = "storage_pressure"
	CodeExpired           = "application_expired"
	CodeCommandTimeout    = "command_timeout"
	CodeCanceled          = "request_canceled"
	CodeInternal          = "internal_error"
)

// Error is the stable failure vocabulary shared by the Manager and HTTP seam.
type Error struct {
	Code       string
	Message    string
	RetryAfter time.Duration
	Cause      error
}

func (err *Error) Error() string {
	if err.Cause == nil {
		return err.Message
	}
	return fmt.Sprintf("%s: %v", err.Message, err.Cause)
}

func (err *Error) Unwrap() error { return err.Cause }

func errorCode(err error) string {
	var sandboxErr *Error
	if errors.As(err, &sandboxErr) {
		return sandboxErr.Code
	}
	return CodeInternal
}

// Identity is trusted request context supplied by the agent runtime, never by
// model-controlled JSON.
type Identity struct {
	SessionID string
	RunID     string
}

// Application is the complete persisted lifecycle record. SessionID,
// container IDs, volume names, and seen run IDs are never serialized to agents.
type Application struct {
	ID                 string        `json:"id"`
	SessionID          string        `json:"session_id"`
	VolumeName         string        `json:"volume_name"`
	ContainerID        string        `json:"container_id,omitempty"`
	State              string        `json:"state"`
	CreatedAt          time.Time     `json:"created_at"`
	LastUsedAt         time.Time     `json:"last_used_at,omitempty"`
	IdleExpiresAt      time.Time     `json:"idle_expires_at,omitempty"`
	HardExpiresAt      time.Time     `json:"hard_expires_at"`
	IdleTTL            time.Duration `json:"idle_ttl"`
	SeenRunIDs         []string      `json:"seen_run_ids,omitempty"`
	WorkspaceBytes     int64         `json:"workspace_bytes,omitempty"`
	WorkspacePreserved bool          `json:"workspace_preserved"`
	Revision           uint64        `json:"revision"`
}

// ApplicationView is the only lifecycle representation returned to callers.
type ApplicationView struct {
	ID                 string `json:"id"`
	State              string `json:"state"`
	CreatedAt          string `json:"created_at"`
	LastUsedAt         string `json:"last_used_at,omitempty"`
	IdleExpiresAt      string `json:"idle_expires_at,omitempty"`
	HardExpiresAt      string `json:"hard_expires_at"`
	IdleTTLSeconds     int64  `json:"idle_ttl_seconds"`
	DistinctRuns       int    `json:"distinct_runs"`
	WorkspaceBytes     int64  `json:"workspace_bytes"`
	WorkspacePreserved bool   `json:"workspace_preserved"`
}

type ApplyResult struct {
	Application ApplicationView `json:"application"`
	Created     bool            `json:"created"`
}

type ApplicationResult struct {
	Application     ApplicationView `json:"application"`
	QueueDurationMS float64         `json:"queue_duration_ms"`
}

type ExecRequest struct {
	Command        string `json:"command"`
	CWD            string `json:"cwd,omitempty"`
	Stdin          string `json:"stdin,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type ExecResult struct {
	Application ApplicationView `json:"application"`
	ExitCode    int             `json:"exit_code"`
	Stdout      string          `json:"stdout"`
	Stderr      string          `json:"stderr"`
	Truncated   bool            `json:"truncated"`
	DurationMS  int64           `json:"duration_ms"`
}

type WriteFileRequest struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Append     bool   `json:"append,omitempty"`
	Executable bool   `json:"executable,omitempty"`
}

type WriteFileResult struct {
	Application ApplicationView `json:"application"`
	Path        string          `json:"path"`
	SizeBytes   int64           `json:"size_bytes"`
}

type ReadFileRequest struct {
	Path     string `json:"path"`
	Offset   int64  `json:"offset,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

type ReadFileResult struct {
	Application ApplicationView `json:"application"`
	Path        string          `json:"path"`
	Content     string          `json:"content"`
	SizeBytes   int64           `json:"size_bytes"`
	NextOffset  int64           `json:"next_offset"`
	EOF         bool            `json:"eof"`
}

// Clock is the time seam used by lifecycle tests and the reaper.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

type Timer interface {
	Channel() <-chan time.Time
	Stop() bool
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTimer(delay time.Duration) Timer {
	return &realClockTimer{timer: time.NewTimer(delay)}
}

type realClockTimer struct {
	timer *time.Timer
}

func (timer *realClockTimer) Channel() <-chan time.Time { return timer.timer.C }
func (timer *realClockTimer) Stop() bool                { return timer.timer.Stop() }

// StateStore persists lifecycle records independently of Docker resources.
type StateStore interface {
	List() ([]Application, error)
	Put(Application) error
	Delete(string) error
	Close() error
}

// Engine is the internal seam around the trusted subset of Docker Engine.
// The real adapter fixes every security-sensitive setting; the fake adapter
// lets Manager tests exercise the same interface.
type Engine interface {
	ValidateRuntimeImage(context.Context, string) error
	CreateVolume(context.Context, string, map[string]string) (bool, error)
	InspectVolume(context.Context, string) (VolumeInfo, error)
	CreateContainer(context.Context, ContainerSpec) (string, error)
	InspectContainer(context.Context, string) (ContainerInfo, error)
	StartContainer(context.Context, string) error
	StopContainer(context.Context, string) error
	RemoveContainer(context.Context, string) error
	RemoveVolume(context.Context, string) error
	Exec(context.Context, string, EngineExecRequest) (EngineExecResult, error)
	ListManaged(context.Context, string) (ManagedResources, error)
	StorageUsage(context.Context, string) (StorageUsage, error)
}

type ContainerSpec struct {
	Name       string
	Image      string
	VolumeName string
	Labels     map[string]string
	CPUs       float64
	Memory     int64
	PIDsLimit  int64
	ShmSize    int64
	TmpSize    int64
}

type ContainerInfo struct {
	ID      string
	Name    string
	Exists  bool
	Running bool
	Labels  map[string]string
}

type VolumeInfo struct {
	Exists bool
	Labels map[string]string
}

type EngineExecRequest struct {
	User        string
	Command     []string
	WorkingDir  string
	Stdin       string
	StdoutLimit int
	StderrLimit int
}

type EngineExecResult struct {
	ExitCode        int
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
}

type ManagedContainer struct {
	ID      string
	Name    string
	Running bool
	Labels  map[string]string
}

type ManagedVolume struct {
	Name   string
	Labels map[string]string
}

type ManagedResources struct {
	Containers []ManagedContainer
	Volumes    []ManagedVolume
}

type StorageUsage struct {
	WorkspaceBytes map[string]int64
	TotalBytes     int64
	FreeBytes      int64
}

func cloneLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		result[key] = value
	}
	return result
}
