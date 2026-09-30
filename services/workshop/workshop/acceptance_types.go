package workshop

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type AcceptanceConfig struct {
	Checks []AcceptanceCheck `json:"checks"`
}
type AcceptanceCheck struct {
	Name           string   `json:"name"`
	Command        []string `json:"command"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// AcceptanceState is the persisted acceptance lifecycle and per-check result.
type AcceptanceState string

const (
	AcceptanceSkipped     AcceptanceState = "skipped"
	AcceptanceRunning     AcceptanceState = "running"
	AcceptancePassed      AcceptanceState = "passed"
	AcceptanceFailed      AcceptanceState = "failed"
	AcceptanceError       AcceptanceState = "error"
	AcceptanceCancelled   AcceptanceState = "cancelled"
	AcceptanceInterrupted AcceptanceState = "interrupted"
)

type Acceptance struct {
	State      AcceptanceState `json:"state"`
	FalseGreen bool            `json:"false_green"`
	Evidence   []Evidence      `json:"evidence"`
}
type Evidence struct {
	ID              string    `json:"id"`
	Check           string    `json:"check"`
	Command         []string  `json:"command"`
	ExitCode        int       `json:"exit_code"`
	TimedOut        bool      `json:"timed_out"`
	DurationMS      int64     `json:"duration_ms"`
	OutputBytes     int64     `json:"output_bytes"`
	OutputTruncated bool      `json:"output_truncated"`
	WorkspaceSHA256 string    `json:"workspace_sha256"`
	Time            time.Time `json:"time"`
}
type AcceptanceEvent struct {
	State      AcceptanceState `json:"state"`
	Check      string          `json:"check,omitempty"`
	ExitCode   *int            `json:"exit_code,omitempty"`
	EvidenceID string          `json:"evidence_id,omitempty"`
	FalseGreen bool            `json:"false_green,omitempty"`
}

var checkName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func validateAcceptance(config *AcceptanceConfig) error {
	if config == nil {
		return nil
	}
	if len(config.Checks) < 1 || len(config.Checks) > 8 {
		return fmt.Errorf("%w: acceptance needs 1..8 checks", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, check := range config.Checks {
		if !checkName.MatchString(check.Name) || seen[check.Name] || len(check.Command) == 0 || !filepath.IsAbs(check.Command[0]) || check.TimeoutSeconds < 1 || check.TimeoutSeconds > 1800 {
			return fmt.Errorf("%w: invalid acceptance check", ErrInvalid)
		}
		for _, arg := range check.Command {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("%w: invalid check argument", ErrInvalid)
			}
		}
		seen[check.Name] = true
	}
	return nil
}
func cloneAcceptance(config *AcceptanceConfig) *AcceptanceConfig {
	if config == nil {
		return nil
	}
	out := &AcceptanceConfig{Checks: append([]AcceptanceCheck{}, config.Checks...)}
	for i := range out.Checks {
		out.Checks[i].Command = append([]string{}, out.Checks[i].Command...)
	}
	return out
}
func skippedAcceptance() *Acceptance {
	return &Acceptance{State: AcceptanceSkipped, Evidence: []Evidence{}}
}
func acceptanceSummary(run Run) (state AcceptanceState, falseGreen bool, count int) {
	if run.Acceptance == nil {
		return AcceptanceSkipped, false, 0
	}
	return run.Acceptance.State, run.Acceptance.FalseGreen, len(run.Acceptance.Evidence)
}
