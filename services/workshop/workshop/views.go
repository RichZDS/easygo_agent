package workshop

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	summaryTextBytes         = 16 * 1024
	summaryErrorBytes        = 4 * 1024
	summaryArtifactCount     = 32
	summaryArtifactPathBytes = 1024
	defaultResultBytes       = 8192
	maxResultBytes           = 32768
)

// TaskSummary omits private workspace/configuration and earlier run payloads.
// Truncation flags describe previews; raw operator endpoints retain full data.
type TaskSummary struct {
	RunIDs    []string     `json:"run_ids"`
	Runtime   string       `json:"runtime,omitempty"`
	Engine    string       `json:"engine"`
	Model     string       `json:"model"`
	ID        string       `json:"id"`
	Namespace string       `json:"namespace"`
	Status    Status       `json:"status"`
	RunCount  int          `json:"run_count"`
	Runs      []RunSummary `json:"runs"`
}

type RunSummary struct {
	Outcome            string     `json:"outcome,omitempty"`
	AcceptanceState    string     `json:"acceptance_state"`
	FalseGreen         bool       `json:"false_green"`
	EvidenceCount      int        `json:"evidence_count"`
	ID                 string     `json:"id"`
	Status             Status     `json:"status"`
	Error              string     `json:"error,omitempty"`
	ErrorTruncated     bool       `json:"error_truncated"`
	Text               string     `json:"text,omitempty"`
	TextBytes          int        `json:"text_bytes"`
	TextTruncated      bool       `json:"text_truncated"`
	ArtifactCount      int        `json:"artifact_count"`
	Artifacts          []Artifact `json:"artifacts"`
	ArtifactsTruncated bool       `json:"artifacts_truncated"`
}

// TaskMetadata has no result text, errors, or artifact arrays, so a list page
// stays bounded independently of result size and accumulated run history.
type TaskMetadata struct {
	Runtime   string        `json:"runtime,omitempty"`
	Engine    string        `json:"engine"`
	Model     string        `json:"model"`
	ID        string        `json:"id"`
	Namespace string        `json:"namespace"`
	Status    Status        `json:"status"`
	RunCount  int           `json:"run_count"`
	Runs      []RunMetadata `json:"runs"`
}

type RunMetadata struct {
	Outcome         string `json:"outcome,omitempty"`
	AcceptanceState string `json:"acceptance_state"`
	FalseGreen      bool   `json:"false_green"`
	EvidenceCount   int    `json:"evidence_count"`
	ID              string `json:"id"`
	Status          Status `json:"status"`
	TextBytes       int    `json:"text_bytes"`
	ArtifactCount   int    `json:"artifact_count"`
}

type TaskPage struct {
	Tasks      []TaskMetadata `json:"tasks"`
	Offset     int            `json:"offset"`
	NextOffset *int           `json:"next_offset"` // null at the end
}

type ResultPage struct {
	TaskID     string `json:"task_id"`
	RunID      string `json:"run_id"`
	Text       string `json:"text"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	TotalBytes int    `json:"total_bytes"`
	EOF        bool   `json:"eof"`
}

func prefix(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func summarize(task *Task) TaskSummary {
	out := TaskSummary{Runtime: task.Workflow.Runtime, Engine: task.Workflow.Engine, Model: task.Workflow.Model, ID: task.ID, Namespace: task.Namespace, Status: task.Status, RunCount: len(task.Runs), Runs: []RunSummary{}, RunIDs: []string{}}
	for _, run := range task.Runs {
		out.RunIDs = append(out.RunIDs, run.ID)
	}
	if len(task.Runs) == 0 {
		return out
	}
	run := task.Runs[len(task.Runs)-1]
	r := RunSummary{Outcome: run.Outcome, AcceptanceState: "skipped", ID: run.ID, Status: run.Status, Error: prefix(run.Error, summaryErrorBytes), Text: prefix(run.Text, summaryTextBytes), TextBytes: len(run.Text), ArtifactCount: len(run.Artifacts), Artifacts: []Artifact{}}
	r.ErrorTruncated = len(r.Error) < len(run.Error)
	r.TextTruncated = len(r.Text) < len(run.Text)
	for _, artifact := range run.Artifacts {
		// Skip oversized paths rather than return a shortened, misleading path.
		if len(r.Artifacts) == summaryArtifactCount {
			break
		}
		if len(artifact.Path) > summaryArtifactPathBytes {
			continue
		}
		r.Artifacts = append(r.Artifacts, artifact)
	}
	r.ArtifactsTruncated = len(r.Artifacts) < len(run.Artifacts)
	out.Runs = append(out.Runs, r)
	return out
}

// Summary returns a bounded latest-attempt view scoped to its trusted owner.
func (s *Service) Summary(namespace, id string) (*TaskSummary, error) {
	task, err := s.Get(namespace, id)
	if err != nil {
		return nil, err
	}
	view := summarize(task)
	return &view, nil
}

// ListPage sorts by creation time then ID. NextOffset is nil at the end.
func (s *Service) ListPage(namespace string, offset, limit int) (*TaskPage, error) {
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	tasks, err := s.List(namespace)
	if err != nil {
		return nil, err
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	if offset > len(tasks) {
		return nil, ErrInvalid
	}
	end := offset + min(limit, len(tasks)-offset)
	page := &TaskPage{Tasks: []TaskMetadata{}, Offset: offset}
	if end < len(tasks) {
		page.NextOffset = &end
	}
	for _, task := range tasks[offset:end] {
		item := TaskMetadata{Runtime: task.Workflow.Runtime, Engine: task.Workflow.Engine, Model: task.Workflow.Model, ID: task.ID, Namespace: task.Namespace, Status: task.Status, RunCount: len(task.Runs), Runs: []RunMetadata{}}
		if len(task.Runs) > 0 {
			run := task.Runs[len(task.Runs)-1]
			item.Runs = append(item.Runs, RunMetadata{Outcome: run.Outcome, AcceptanceState: "skipped", ID: run.ID, Status: run.Status, TextBytes: len(run.Text), ArtifactCount: len(run.Artifacts)})
		}
		page.Tasks = append(page.Tasks, item)
	}
	return page, nil
}

// Result returns complete, lossless UTF-8 pages from one owned run's result.
// runID="" selects the latest attempt; pin the returned ID for later pages.
func (s *Service) Result(namespace, id, runID string, offset, limit int) (*ResultPage, error) {
	if offset < 0 || limit < 4 || limit > maxResultBytes {
		return nil, ErrInvalid
	}
	task, err := s.Get(namespace, id)
	if err != nil {
		return nil, err
	}
	var run *Run
	if runID == "" && len(task.Runs) > 0 {
		run = &task.Runs[len(task.Runs)-1]
	} else {
		for i := range task.Runs {
			if task.Runs[i].ID == runID {
				run = &task.Runs[i]
				break
			}
		}
	}
	if run == nil {
		return nil, ErrNotFound
	}
	text := run.Text
	if offset > len(text) || (offset < len(text) && !utf8.RuneStart(text[offset])) || !utf8.ValidString(text) {
		return nil, ErrInvalid
	}
	part := prefix(text[offset:], limit)
	next := offset + len(part)
	return &ResultPage{TaskID: id, RunID: run.ID, Text: part, Offset: offset, NextOffset: next, TotalBytes: len(text), EOF: next == len(text)}, nil
}

const maxArtifactDownloadBytes = 8 * 1024 * 1024

var ErrArtifactTooLarge = errors.New("artifact exceeds 8 MiB download limit")

type ArtifactDownload struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	DataBase64 string `json:"data_base64"`
}

// Artifact returns bytes only for a recorded artifact of an owned, terminal task.
// The lock spans lookup, descriptor read, digest verification and encoding so a
// resume/start cannot mutate the workspace while a download is being assembled.
// All filesystem/storage failures are translated into constant domain errors.
func (s *Service) Artifact(namespace, id, runID, path string) (*ArtifactDownload, error) {
	if namespace == "" || id == "" || !filepath.IsLocal(path) || path == "." || filepath.Clean(path) != path || strings.ContainsAny(path, "\\\x00") {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.available() != nil {
		return nil, ErrClosed
	}
	task, err := s.Get(namespace, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, ErrClosed
	}
	if !downloadTerminal(task.Status) {
		return nil, ErrConflict
	}
	var run *Run
	if runID == "" && len(task.Runs) > 0 {
		run = &task.Runs[len(task.Runs)-1]
	} else {
		for i := range task.Runs {
			if task.Runs[i].ID == runID {
				run = &task.Runs[i]
				break
			}
		}
	}
	if run == nil {
		return nil, ErrNotFound
	}
	if !downloadTerminal(run.Status) {
		return nil, ErrConflict
	}
	var recorded *Artifact
	for i := range run.Artifacts {
		if run.Artifacts[i].Path == path {
			recorded = &run.Artifacts[i]
			break
		}
	}
	if recorded == nil {
		return nil, ErrNotFound
	}
	if recorded.Size < 0 || len(recorded.SHA256) != 64 {
		return nil, ErrConflict
	}
	if recorded.Size > maxArtifactDownloadBytes {
		return nil, ErrArtifactTooLarge
	}
	taskUUID, err := uuid.Parse(task.ID)
	if err != nil || taskUUID.String() != task.ID {
		return nil, ErrConflict
	}
	workspace := filepath.Join(s.root, "workspaces", task.ID)
	if task.Workspace != workspace {
		return nil, ErrConflict
	}
	f, err := openArtifactDownload(workspace, path)
	if err != nil {
		return nil, ErrConflict
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != recorded.Size {
		return nil, ErrConflict
	}
	data, err := io.ReadAll(io.LimitReader(f, maxArtifactDownloadBytes+1))
	if err != nil || int64(len(data)) != recorded.Size {
		return nil, ErrConflict
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != recorded.SHA256 {
		return nil, ErrConflict
	}
	return &ArtifactDownload{Path: recorded.Path, SHA256: recorded.SHA256, Size: recorded.Size, DataBase64: base64.StdEncoding.EncodeToString(data)}, nil
}

func downloadTerminal(status Status) bool {
	switch status {
	case Succeeded, Failed, Cancelled, TimedOut, Interrupted:
		return true
	default:
		return false
	}
}
