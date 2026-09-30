package rpc

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseEnvelope(t *testing.T) {
	long := strings.Repeat("i", 128)
	for _, tc := range []struct {
		name, raw, id, method, namespace, code string
	}{
		{"valid", `{"jsonrpc":"2.0","id":"a","method":"m.x","params":{"namespace":"tenant-a","n":1}}`, "a", "m.x", "tenant-a", ""},
		{"longest id", `{"jsonrpc":"2.0","id":"` + long + `","method":"m","params":{"namespace":"n"}}`, long, "m", "n", ""},
		{"missing namespace", `{"jsonrpc":"2.0","id":"a","method":"m","params":{}}`, "a", "m", "", ""},
		{"invalid namespace", `{"jsonrpc":"2.0","id":"a","method":"m","params":{"namespace":"bad/ns"}}`, "a", "m", "", ""},
		{"non-string namespace", `{"jsonrpc":"2.0","id":"a","method":"m","params":{"namespace":1}}`, "a", "m", "", ""},
		{"not JSON", `{`, "", "", "", "parse_error"},
		{"array", `[]`, "", "", "", "invalid_request"},
		{"null", `null`, "", "", "", "invalid_request"},
		{"unknown field", `{"jsonrpc":"2.0","id":"a","method":"m","params":{},"extra":0}`, "", "", "", "invalid_request"},
		{"wrong case", `{"jsonrpc":"2.0","id":"a","ID":"a","method":"m","params":{}}`, "", "", "", "invalid_request"},
		{"duplicate key", `{"jsonrpc":"2.0","id":"a","method":"m","method":"n","params":{}}`, "", "", "", "invalid_request"},
		{"no id", `{"jsonrpc":"2.0","method":"m","params":{}}`, "", "", "", "invalid_request"},
		{"numeric id", `{"jsonrpc":"2.0","id":1,"method":"m","params":{}}`, "", "", "", "invalid_request"},
		{"empty id", `{"jsonrpc":"2.0","id":"","method":"m","params":{}}`, "", "", "", "invalid_request"},
		{"id too long", `{"jsonrpc":"2.0","id":"` + long + `x","method":"m","params":{}}`, "", "", "", "invalid_request"},
		{"old version", `{"jsonrpc":"1.0","id":"a","method":"m","params":{}}`, "a", "", "", "invalid_request"},
		{"no method", `{"jsonrpc":"2.0","id":"a","params":{}}`, "a", "", "", "invalid_request"},
		{"no params", `{"jsonrpc":"2.0","id":"a","method":"m"}`, "a", "", "", "invalid_request"},
		{"array params", `{"jsonrpc":"2.0","id":"a","method":"m","params":[]}`, "a", "", "", "invalid_request"},
		{"null params", `{"jsonrpc":"2.0","id":"a","method":"m","params":null}`, "a", "", "", "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, req, namespace, e := parseEnvelope([]byte(tc.raw))
			code := ""
			if e != nil {
				code = e.Data.Code
			}
			if id != tc.id || namespace != tc.namespace || code != tc.code || e == nil && req.Method != tc.method {
				t.Fatalf("got id=%q method=%q namespace=%q err=%+v", id, req.Method, namespace, e)
			}
		})
	}
}

type failingWriter struct{ *httptest.ResponseRecorder }

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("peer gone") }

func TestFinishStream(t *testing.T) {
	failure := Failure(-32000, "upstream_error")
	for _, tc := range []struct {
		name     string
		prepare  func(*Stream)
		broken   bool
		rpcErr   *Error
		code     string
		streamed bool
		event    string // terminal event finishStream must add, if any
	}{
		{"nothing streamed", nil, false, nil, "", false, ""},
		{"nothing streamed, method error", nil, false, failure, "upstream_error", false, ""},
		{"result sent", func(s *Stream) { _ = s.Delta("d"); _ = s.Result("r") }, false, nil, "", true, ""},
		{"result sent, method error", func(s *Stream) { _ = s.Result("r") }, false, failure, "upstream_error", true, ""},
		{"deltas without result", func(s *Stream) { _ = s.Delta("d") }, false, nil, "missing_terminal", true, "missing_terminal"},
		{"deltas then method error", func(s *Stream) { _ = s.Delta("d") }, false, failure, "upstream_error", true, "upstream_error"},
		{"write failed after result", func(s *Stream) { _ = s.Result("r") }, true, nil, "stream_write_error", true, ""},
		{"write failed before result", func(s *Stream) { _ = s.Delta("d") }, true, nil, "missing_terminal", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			var w http.ResponseWriter = rec
			if tc.broken {
				w = failingWriter{rec}
			}
			s := &Stream{w: w, id: "req-1"}
			if tc.prepare != nil {
				tc.prepare(s)
			}
			before := rec.Body.String()
			code, streamed := finishStream(s, tc.rpcErr)
			if code != tc.code || streamed != tc.streamed || streamed && !s.terminal {
				t.Fatalf("finishStream = %q %v (terminal %v), want %q %v", code, streamed, s.terminal, tc.code, tc.streamed)
			}
			added := strings.TrimPrefix(rec.Body.String(), before)
			if tc.event == "" && added != "" || tc.event != "" && (!strings.HasPrefix(added, "event: error\n") || !strings.Contains(added, `"code":"`+tc.event+`"`) || !strings.Contains(added, `"id":"req-1"`)) {
				t.Fatalf("terminal event %q, want %q", added, tc.event)
			}
		})
	}
}
