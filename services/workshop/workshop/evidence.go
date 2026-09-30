package workshop

import (
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type EvidenceList struct {
	Namespace       string          `json:"namespace"`
	TaskID          string          `json:"task_id"`
	RunID           string          `json:"run_id"`
	AcceptanceState AcceptanceState `json:"acceptance_state"`
	FalseGreen      bool            `json:"false_green"`
	Evidence        []Evidence      `json:"evidence"`
}
type EvidencePage struct {
	Namespace  string `json:"namespace"`
	TaskID     string `json:"task_id"`
	RunID      string `json:"run_id"`
	EvidenceID string `json:"evidence_id"`
	Text       string `json:"text"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	TotalBytes int    `json:"total_bytes"`
	EOF        bool   `json:"eof"`
}

// Evidence returns recorded metadata or one bounded output page. All identities
// resolve through an owned task/run before a controller-chosen file is opened.
func (s *Service) Evidence(namespace, id, runID, evidenceID string, offset, limit int) (any, error) {
	if offset < 0 || limit < 4 || limit > 32768 {
		return nil, ErrInvalid
	}
	task, err := s.Get(namespace, id)
	if err != nil {
		return nil, err
	}
	run := task.run(runID)
	if run == nil {
		return nil, ErrNotFound
	}
	state, green, _ := acceptanceSummary(*run)
	evidence := []Evidence{}
	if run.Acceptance != nil {
		evidence = append(evidence, run.Acceptance.Evidence...)
	}
	if evidenceID == "" {
		return EvidenceList{Namespace: namespace, TaskID: id, RunID: run.ID, AcceptanceState: state, FalseGreen: green, Evidence: evidence}, nil
	}
	found := false
	for _, item := range evidence {
		if item.ID == evidenceID {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	file, err := openArtifactDownload(s.root, filepath.Join("evidence", task.ID, evidenceID+".log"))
	if err != nil {
		return nil, ErrNotFound
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > evidenceLimit {
		return nil, ErrConflict
	}
	raw, err := io.ReadAll(io.LimitReader(file, evidenceLimit+1))
	if err != nil || len(raw) > evidenceLimit {
		return nil, ErrConflict
	}
	if offset > len(raw) {
		return nil, ErrInvalid
	}
	// Offsets refer to stored bytes. Preserve valid UTF-8 runes at boundaries;
	// replace malformed check output only in the JSON text representation.
	if !evidenceBoundary(raw, offset) {
		return nil, ErrInvalid
	}
	end := min(len(raw), offset+limit)
	for end > offset && !evidenceBoundary(raw, end) {
		end--
	}
	text := strings.ToValidUTF8(string(raw[offset:end]), "\uFFFD")
	return EvidencePage{Namespace: namespace, TaskID: id, RunID: run.ID, EvidenceID: evidenceID, Text: text, Offset: offset, NextOffset: end, TotalBytes: len(raw), EOF: end == len(raw)}, nil
}

func evidenceBoundary(raw []byte, offset int) bool {
	for start := max(0, offset-3); start < offset; start++ {
		if utf8.RuneStart(raw[start]) {
			_, size := utf8.DecodeRune(raw[start:])
			if size > 1 && start+size > offset {
				return false
			}
		}
	}
	return true
}
