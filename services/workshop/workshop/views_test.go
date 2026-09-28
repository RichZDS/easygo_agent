//go:build linux

package workshop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func viewCall(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer view-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestResultPagesReconstructUTF8AndHistoricalRun(t *testing.T) {
	s, text := seedViewTasks(t)
	handler := Handler(s, "view-token")
	var joined strings.Builder
	offset := 0
	for {
		w := viewCall(t, handler, "GET", fmt.Sprintf("/v1/tasks/task-0/result?namespace=owner&run_id=latest-0&offset=%d&limit=32767", offset), "")
		if w.Code != 200 {
			t.Fatalf("page: %d %s", w.Code, w.Body.String())
		}
		if w.Body.Len() >= 512*1024 {
			t.Fatal("result page too large")
		}
		var page ResultPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
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
	w := viewCall(t, handler, "GET", fmt.Sprintf("/v1/tasks/task-0/result?namespace=owner&offset=%d", offset), "")
	var last ResultPage
	if err := json.Unmarshal(w.Body.Bytes(), &last); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !last.EOF || last.Text != "" || last.NextOffset != offset {
		t.Fatalf("end cursor: %d %+v", w.Code, last)
	}
	w = viewCall(t, handler, "GET", "/v1/tasks/task-0/result?namespace=owner&run_id=historical-0&limit=4", "")
	var history ResultPage
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || history.Text != "历" || history.TotalBytes != len("历史🙂") || history.NextOffset != 3 || history.EOF {
		t.Fatalf("historical run: %d %+v", w.Code, history)
	}
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/v1/tasks/task-0/result?namespace=other", 404},
		{"/v1/tasks/missing/result?namespace=owner", 404},
		{"/v1/tasks/task-0/result?namespace=owner&run_id=latest-1", 404},
		{"/v1/tasks/task-0/result?namespace=owner&offset=2", 400},
		{"/v1/tasks/task-0/result?namespace=owner&offset=99999999", 400},
		{"/v1/tasks/task-0/result?namespace=owner&offset=-1", 400},
		{"/v1/tasks/task-0/result?namespace=owner&limit=0", 400},
		{"/v1/tasks/task-0/result?namespace=owner&limit=3", 400},
		{"/v1/tasks/task-0/result?namespace=owner&limit=32769", 400},
		{"/v1/tasks/task-0/result?namespace=owner&limit=bad", 400},
		{"/v1/tasks/task-0/result?namespace=owner&offset=1.5", 400},
		{"/v1/tasks/task-0/result?namespace=owner&limit=", 400},
		{"/v1/tasks/task-0/result?namespace=owner&limit=4&limit=8", 400},
	} {
		w = viewCall(t, handler, "GET", tc.path, "")
		if w.Code != tc.code {
			t.Errorf("%s: want %d got %d", tc.path, tc.code, w.Code)
		}
	}
}

func TestSummaryHTTPAndMetadataListPagination(t *testing.T) {
	s, text := seedViewTasks(t)
	handler := Handler(s, "view-token")
	w := viewCall(t, handler, "GET", "/v1/tasks/task-0?namespace=owner&view=summary", "")
	var summary TaskSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || summary.RunCount != 2 || len(summary.Runs) != 1 || !summary.Runs[0].TextTruncated || summary.Runs[0].TextBytes != len(text) || w.Body.Len() >= 512*1024 {
		t.Fatal("invalid bounded task summary")
	}
	raw := viewCall(t, handler, "GET", "/v1/tasks/task-0?namespace=owner", "")
	var task Task
	if err := json.Unmarshal(raw.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if raw.Code != 200 || len(task.Runs) != 2 || task.Runs[1].Text != text {
		t.Fatal("raw operator response changed")
	}
	ids := []string{}
	for offset := 0; ; {
		w = viewCall(t, handler, "GET", fmt.Sprintf("/v1/tasks?namespace=owner&view=summary&offset=%d&limit=2", offset), "")
		if w.Code != 200 || w.Body.Len() > 4096 {
			t.Fatalf("list not bounded: %d bytes=%d", w.Code, w.Body.Len())
		}
		for _, field := range []string{`"text":`, `"error":`, `"artifacts":`} {
			if strings.Contains(w.Body.String(), field) {
				t.Fatalf("list includes payload %s", field)
			}
		}
		var page TaskPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
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
	for _, query := range []string{"limit=0", "limit=101", "limit=bad", "limit=", "offset=-1", "offset=999999999999999999999999999", "offset=5"} {
		w = viewCall(t, handler, "GET", "/v1/tasks?namespace=owner&view=summary&"+query, "")
		if w.Code != 400 {
			t.Errorf("invalid list query %s: %d", query, w.Code)
		}
	}
	w = viewCall(t, handler, "GET", "/v1/tasks?namespace=owner&view=summary&offset=4", "")
	var end TaskPage
	if err := json.Unmarshal(w.Body.Bytes(), &end); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(end.Tasks) != 0 || end.NextOffset != nil {
		t.Fatal("list end cursor rejected")
	}
}

func TestMutationSummaryView(t *testing.T) {
	s := newFixtureService(t, fixtureConfig(t, "codex"))
	handler := Handler(s, "view-token")
	w := viewCall(t, handler, "POST", "/v1/tasks?view=invalid", `{"workflow":"note","input":"success"}`)
	if w.Code != 400 {
		t.Fatal("invalid view accepted")
	}
	list, err := s.List("operator")
	if err != nil || len(list) != 0 {
		t.Fatal("invalid view caused side effect")
	}
	w = viewCall(t, handler, "POST", "/v1/tasks?view=summary", `{"workflow":"note","input":"success"}`)
	var summary TaskSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if w.Code != 202 || summary.RunCount != 1 || strings.Contains(w.Body.String(), "workspace") {
		t.Fatalf("submit summary: %d %s", w.Code, w.Body.String())
	}
	waitTask(t, s, "operator", summary.ID, func(t *Task) bool { return terminal(t.Status) })
	w = viewCall(t, handler, "POST", "/v1/tasks/"+summary.ID+"/resume?view=summary", `{"input":"hang"}`)
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if w.Code != 202 || summary.RunCount != 2 || len(summary.Runs) != 1 || summary.Runs[0].TextBytes != 0 {
		t.Fatal("resume summary retained old payload")
	}
	w = viewCall(t, handler, "POST", "/v1/tasks/"+summary.ID+"/cancel?view=summary", "")
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || summary.RunCount != 2 || len(summary.Runs) != 1 {
		t.Fatal("cancel summary failed")
	}
}
