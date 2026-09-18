package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"easygo-agent/internal/task"
)

func taskError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, task.ErrNotFound):
		code = 404
	case errors.Is(err, task.ErrBusy), errors.Is(err, task.ErrConflict):
		code = 409
	case errors.Is(err, task.ErrInvalid):
		code = 400
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
func registerTasks(mux *http.ServeMux, h *Handler, service *task.Service) {
	base := "/v1/users/{user}/sessions/{id}/tasks"
	handle := func(fn func(http.ResponseWriter, *http.Request, task.Owner)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			o := task.Owner{User: r.PathValue("user"), Session: r.PathValue("id")}
			// Validate the owning session even for an empty task list.
			if _, err := h.store.History(r.Context(), o.User, o.Session, 0, 1); err != nil {
				writeError(w, err)
				return
			}
			fn(w, r, o)
		}
	}
	mux.HandleFunc("GET "+base, handle(func(w http.ResponseWriter, r *http.Request, o task.Owner) {
		items, err := service.Store.List(r.Context(), o)
		if err != nil {
			taskError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"tasks": items})
	}))
	mux.HandleFunc("GET "+base+"/{task_id}", handle(func(w http.ResponseWriter, r *http.Request, o task.Owner) {
		t, err := service.Store.Get(r.Context(), o, r.PathValue("task_id"))
		if err != nil {
			taskError(w, err)
			return
		}
		if r.URL.Query().Get("format") == "markdown" {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = w.Write([]byte(task.Markdown(t)))
			return
		}
		writeJSON(w, 200, t)
	}))
	mux.HandleFunc("DELETE "+base+"/{task_id}", handle(func(w http.ResponseWriter, r *http.Request, o task.Owner) {
		t, err := service.Store.Cancel(r.Context(), o, r.PathValue("task_id"))
		if err != nil {
			taskError(w, err)
			return
		}
		writeJSON(w, 200, t)
	}))
	mux.HandleFunc("POST "+base+"/{task_id}/resume", handle(func(w http.ResponseWriter, r *http.Request, o task.Owner) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var in task.Resume
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&in); err != nil {
			writeJSON(w, 400, map[string]string{"error": "expected resume instructions and explicit recovery decisions"})
			return
		}
		if d.Decode(new(any)) != io.EOF {
			writeJSON(w, 400, map[string]string{"error": "expected one JSON object"})
			return
		}
		t, err := service.Store.Resume(r.Context(), o, r.PathValue("task_id"), in)
		if err != nil {
			taskError(w, err)
			return
		}
		writeJSON(w, 202, t)
	}))
	mux.HandleFunc("GET "+base+"/{task_id}/events", handle(func(w http.ResponseWriter, r *http.Request, o task.Owner) {
		after, err := strconv.ParseInt(defaultValue(r.URL.Query().Get("after"), "0"), 10, 64)
		if err != nil || after < 0 {
			writeJSON(w, 400, map[string]string{"error": "after must be a nonnegative event sequence"})
			return
		}
		t, err := service.Store.Get(r.Context(), o, r.PathValue("task_id"))
		if err != nil {
			taskError(w, err)
			return
		}
		events := []task.Event{}
		for _, e := range t.Events {
			if e.Seq > after {
				events = append(events, e)
				if len(events) == 100 {
					break
				}
			}
		}
		if len(events) > 0 {
			after = events[len(events)-1].Seq
		}
		writeJSON(w, 200, map[string]any{"events": events, "next_after": after, "status": t.Status})
	}))
}
