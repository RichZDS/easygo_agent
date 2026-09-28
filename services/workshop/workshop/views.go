package workshop

import (
	"sort"
	"unicode/utf8"
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
	ID            string `json:"id"`
	Status        Status `json:"status"`
	TextBytes     int    `json:"text_bytes"`
	ArtifactCount int    `json:"artifact_count"`
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
	out := TaskSummary{Runtime: task.Workflow.Runtime, Engine: task.Workflow.Engine, Model: task.Workflow.Model, ID: task.ID, Namespace: task.Namespace, Status: task.Status, RunCount: len(task.Runs), Runs: []RunSummary{}}
	if len(task.Runs) == 0 {
		return out
	}
	run := task.Runs[len(task.Runs)-1]
	r := RunSummary{ID: run.ID, Status: run.Status, Error: prefix(run.Error, summaryErrorBytes), Text: prefix(run.Text, summaryTextBytes), TextBytes: len(run.Text), ArtifactCount: len(run.Artifacts), Artifacts: []Artifact{}}
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
			item.Runs = append(item.Runs, RunMetadata{ID: run.ID, Status: run.Status, TextBytes: len(run.Text), ArtifactCount: len(run.Artifacts)})
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
