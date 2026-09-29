package workshop

import (
	"encoding/json"
	"io"
	"net/http"
	"unicode/utf8"

	"easygo-agent/rpc"
)

func serveCrew(w http.ResponseWriter, r *http.Request, crew CrewChannel) {
	if r.Method != "POST" || r.URL.RawQuery != "" || r.URL.ForceQuery || (r.URL.Path != "/crew/messages" && r.URL.Path != "/crew/inbox") {
		http.NotFound(w, r)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || !utf8.Valid(raw) {
		http.Error(w, "invalid crew request", 400)
		return
	}
	var result any
	if r.URL.Path == "/crew/messages" {
		var p CrewPost
		var fields map[string]json.RawMessage
		if rpc.Decode(raw, &p) != nil || validateCrewPost(p) != nil {
			http.Error(w, "invalid crew message", 400)
			return
		}
		_ = json.Unmarshal(raw, &fields)
		if _, present := fields["claims"]; present && p.Kind != "submit" {
			http.Error(w, "claims only allowed for submit", 400)
			return
		}
		result, err = crew.Post(r.Context(), p)
	} else {
		var p struct {
			After uint64 `json:"after"`
		}
		if rpc.Decode(raw, &p) != nil {
			http.Error(w, "invalid inbox request", 400)
			return
		}
		result, err = crew.Inbox(r.Context(), p.After)
	}
	if err != nil {
		http.Error(w, "crew request rejected", crewHTTPStatus(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(result)
}
