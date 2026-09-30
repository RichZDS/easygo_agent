//go:build linux

package workshop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

func TestSummaryBoundsAndExplicitPreviews(t *testing.T) {
	text := strings.Repeat("x", summaryTextBytes-1) + "界🙂" + strings.Repeat("z", 1024*1024)
	longError := strings.Repeat("<", summaryErrorBytes-1) + "界" + strings.Repeat("&", summaryErrorBytes)
	artifacts := []Artifact{{Path: strings.Repeat("x", summaryArtifactPathBytes+1), SHA256: strings.Repeat("a", 64)}}
	for i := 0; i < 100; i++ {
		artifacts = append(artifacts, Artifact{Path: strings.Repeat("<", summaryArtifactPathBytes), Size: 1, SHA256: strings.Repeat("b", 64)})
	}
	task := &Task{ID: "task", Namespace: strings.Repeat("<", 256), Status: Succeeded, Runs: []Run{{ID: "old", Text: "old"}, {ID: "latest", Status: Succeeded, Text: text, Error: longError, Artifacts: artifacts}}}
	summary := summarize(task)
	if summary.RunCount != 2 || len(summary.Runs) != 1 || summary.Runs[0].ID != "latest" {
		t.Fatalf("attempt selection: %+v", summary)
	}
	run := summary.Runs[0]
	if run.TextBytes != len(text) || !run.TextTruncated || !utf8.ValidString(run.Text) || len(run.Text) != summaryTextBytes-1 {
		t.Fatal("text prefix or truncation metadata incorrect")
	}
	if !run.ErrorTruncated || !utf8.ValidString(run.Error) || len(run.Error) != summaryErrorBytes-1 {
		t.Fatal("error prefix incorrect")
	}
	if run.ArtifactCount != 101 || !run.ArtifactsTruncated || len(run.Artifacts) != summaryArtifactCount {
		t.Fatal("artifact count/truncation incorrect")
	}
	for _, artifact := range run.Artifacts {
		if artifact.Path != strings.Repeat("<", summaryArtifactPathBytes) {
			t.Fatal("artifact path shortened instead of skipped")
		}
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 512*1024 {
		t.Fatalf("summary exceeds bound even before transport: %d", len(raw))
	}
	short := summarize(&Task{Runs: []Run{{Text: "完整", Error: "error", Artifacts: []Artifact{{Path: "note.md"}}}}}).Runs[0]
	if short.Text != "完整" || short.TextTruncated || short.ErrorTruncated || short.ArtifactsTruncated {
		t.Fatal("short output changed or marked truncated")
	}
}

func seedViewTasks(t *testing.T) (*Service, string) {
	t.Helper()
	s := newFixtureService(t, fixtureConfig(t, "codex"))
	text := strings.Repeat("a界🙂", 200000)
	err := s.db.Update(func(tx *bolt.Tx) error {
		for i := 0; i < 5; i++ {
			namespace := "owner"
			if i == 4 {
				namespace = "other"
			}
			task := &Task{ID: fmt.Sprintf("task-%d", i), Namespace: namespace, Status: Succeeded, CreatedAt: time.Unix(int64(i/2), 0), Runs: []Run{{ID: fmt.Sprintf("historical-%d", i), Status: Succeeded, Text: "历史🙂"}, {ID: fmt.Sprintf("latest-%d", i), Status: Succeeded, Text: text, Error: strings.Repeat("error", 1000), Artifacts: []Artifact{{Path: "note.md", Size: 12, SHA256: strings.Repeat("a", 64)}}}}}
			if err := putTask(tx, task); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, text
}

func TestResultPagesReconstructUTF8AndHistoricalRun(t *testing.T) {
	s, text := seedViewTasks(t)
	var joined strings.Builder
	offset := 0
	for {
		page, err := s.Result("owner", "task-0", "latest-0", offset, 32767)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		if raw, _ := json.Marshal(page); len(raw) >= 512*1024 {
			t.Fatal("result page too large")
		}
		if page.Offset != offset || page.NextOffset != offset+len(page.Text) || page.TotalBytes != len(text) || !utf8.ValidString(page.Text) || page.TaskID != "task-0" || page.RunID != "latest-0" {
			t.Fatalf("invalid page metadata: %+v", page)
		}
		joined.WriteString(page.Text)
		if page.EOF {
			offset = page.NextOffset
			break
		}
		if page.NextOffset <= offset {
			t.Fatal("page did not advance")
		}
		offset = page.NextOffset
	}
	if joined.String() != text {
		t.Fatal("UTF8 pages lost or duplicated bytes")
	}
	last, err := s.Result("owner", "task-0", "", offset, defaultResultBytes)
	if err != nil || !last.EOF || last.Text != "" || last.NextOffset != offset {
		t.Fatalf("end cursor: %v %+v", err, last)
	}
	history, err := s.Result("owner", "task-0", "historical-0", 0, 4)
	if err != nil || history.Text != "历" || history.TotalBytes != len("历史🙂") || history.NextOffset != 3 || history.EOF {
		t.Fatalf("historical run: %v %+v", err, history)
	}
	for _, tc := range []struct {
		name                 string
		namespace, id, runID string
		offset, limit        int
		want                 error
	}{
		{"other namespace", "other", "task-0", "", 0, defaultResultBytes, ErrNotFound},
		{"missing task", "owner", "missing", "", 0, defaultResultBytes, ErrNotFound},
		{"run of another task", "owner", "task-0", "latest-1", 0, defaultResultBytes, ErrNotFound},
		{"offset inside rune", "owner", "task-0", "", 2, defaultResultBytes, ErrInvalid},
		{"offset past end", "owner", "task-0", "", 99999999, defaultResultBytes, ErrInvalid},
		{"negative offset", "owner", "task-0", "", -1, defaultResultBytes, ErrInvalid},
		{"zero limit", "owner", "task-0", "", 0, 0, ErrInvalid},
		{"limit below one rune", "owner", "task-0", "", 0, 3, ErrInvalid},
		{"limit above max", "owner", "task-0", "", 0, maxResultBytes + 1, ErrInvalid},
	} {
		if _, err := s.Result(tc.namespace, tc.id, tc.runID, tc.offset, tc.limit); !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v got %v", tc.name, tc.want, err)
		}
	}
}

func TestSummaryAndMetadataListPagination(t *testing.T) {
	s, text := seedViewTasks(t)
	summary, err := s.Summary("owner", "task-0")
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(summary); summary.RunCount != 2 || len(summary.Runs) != 1 || !summary.Runs[0].TextTruncated || summary.Runs[0].TextBytes != len(text) || len(raw) >= 512*1024 {
		t.Fatal("invalid bounded task summary")
	}
	task, err := s.Get("owner", "task-0")
	if err != nil || len(task.Runs) != 2 || task.Runs[1].Text != text {
		t.Fatal("raw operator response changed")
	}
	ids := []string{}
	for offset := 0; ; {
		page, err := s.ListPage("owner", offset, 2)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		raw, _ := json.Marshal(page)
		if len(raw) > 4096 {
			t.Fatalf("list not bounded: bytes=%d", len(raw))
		}
		for _, field := range []string{`"text":`, `"error":`, `"artifacts":`} {
			if strings.Contains(string(raw), field) {
				t.Fatalf("list includes payload %s", field)
			}
		}
		if page.Offset != offset {
			t.Fatal("list cursor changed")
		}
		for _, item := range page.Tasks {
			ids = append(ids, item.ID)
			if item.Namespace != "owner" || item.RunCount != 2 || len(item.Runs) != 1 || item.Runs[0].TextBytes != len(text) {
				t.Fatal("list metadata missing")
			}
		}
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset <= offset {
			t.Fatal("list cursor did not advance")
		}
		offset = *page.NextOffset
	}
	if strings.Join(ids, ",") != "task-0,task-1,task-2,task-3" {
		t.Fatalf("list order/ownership: %v", ids)
	}
	for _, tc := range []struct{ offset, limit int }{{0, 0}, {0, 101}, {-1, 20}, {5, 20}} {
		if _, err := s.ListPage("owner", tc.offset, tc.limit); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid list page offset=%d limit=%d: %v", tc.offset, tc.limit, err)
		}
	}
	end, err := s.ListPage("owner", 4, 20)
	if err != nil || len(end.Tasks) != 0 || end.NextOffset != nil {
		t.Fatal("list end cursor rejected")
	}
}

// Summaries of mutation results stay bounded: no workspace path and no payload
// carried over from the previous attempt.
func TestMutationSummaryView(t *testing.T) {
	s := newFixtureService(t, fixtureConfig(t, "codex"))
	task, err := s.Submit(SubmitRequest{Namespace: "operator", Workflow: "note", Input: "success"})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summary("operator", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(summary); summary.RunCount != 1 || strings.Contains(string(raw), "workspace") {
		t.Fatalf("submit summary: %s", raw)
	}
	waitTask(t, s, "operator", task.ID, func(t *Task) bool { return terminal(t.Status) })
	if _, err = s.Resume("operator", task.ID, "hang"); err != nil {
		t.Fatal(err)
	}
	if summary, err = s.Summary("operator", task.ID); err != nil || summary.RunCount != 2 || len(summary.Runs) != 1 || summary.Runs[0].TextBytes != 0 {
		t.Fatalf("resume summary retained old payload: %v %+v", err, summary)
	}
	if _, err = s.Cancel("operator", task.ID); err != nil {
		t.Fatal(err)
	}
	if summary, err = s.Summary("operator", task.ID); err != nil || summary.RunCount != 2 || len(summary.Runs) != 1 {
		t.Fatalf("cancel summary failed: %v %+v", err, summary)
	}
}
