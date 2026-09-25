package workshop

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Handler grants operator access to every namespace using ?namespace= (default
// operator). Namespace selection requires a configured bearer. This is trusted
// service-to-service scoping, not multiuser auth. Clients cannot select engines,
// options or working directories.
func Handler(service *Service, bearer string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, service.Workflows(), nil)
	})
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Workflow       string `json:"workflow"`
			Input          string `json:"input"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		task, err := service.Submit(SubmitRequest{Namespace: requestNamespace(r), Workflow: body.Workflow, Input: body.Input, IdempotencyKey: body.IdempotencyKey})
		respondTask(w, r, http.StatusAccepted, task, err)
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("view") == "summary" {
			offset, limit, err := pageParameters(r, 20, 1, 100)
			if err != nil {
				respond(w, 0, nil, err)
				return
			}
			page, err := service.ListPage(requestNamespace(r), offset, limit)
			respond(w, http.StatusOK, page, err)
			return
		}
		tasks, err := service.List(requestNamespace(r))
		respond(w, http.StatusOK, tasks, err)
	})
	mux.HandleFunc("GET /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		task, err := service.Get(requestNamespace(r), r.PathValue("id"))
		respondTask(w, r, http.StatusOK, task, err)
	})
	mux.HandleFunc("GET /v1/tasks/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		offset, limit, err := pageParameters(r, defaultResultBytes, 4, maxResultBytes)
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		page, err := service.Result(requestNamespace(r), r.PathValue("id"), r.URL.Query().Get("run_id"), offset, limit)
		respond(w, http.StatusOK, page, err)
	})
	mux.HandleFunc("GET /v1/tasks/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var after uint64
		if value := r.URL.Query().Get("after"); value != "" {
			var err error
			after, err = strconv.ParseUint(value, 10, 64)
			if err != nil {
				respond(w, 0, nil, ErrInvalid)
				return
			}
		}
		events, err := service.Events(requestNamespace(r), r.PathValue("id"), after)
		respond(w, http.StatusOK, events, err)
	})
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		task, err := service.Cancel(requestNamespace(r), r.PathValue("id"))
		respondTask(w, r, http.StatusOK, task, err)
	})
	mux.HandleFunc("POST /v1/tasks/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input string `json:"input"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		task, err := service.Resume(requestNamespace(r), r.PathValue("id"), body.Input)
		respondTask(w, r, http.StatusAccepted, task, err)
	})
	want := sha256.Sum256([]byte(bearer))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearer != "" {
			header := r.Header.Get("Authorization")
			got := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
			if !strings.HasPrefix(header, "Bearer ") || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if bearer == "" && requestNamespace(r) != "operator" {
			http.Error(w, "namespace selection requires operator bearer", http.StatusUnauthorized)
			return
		}
		if !validInput(requestNamespace(r), "") {
			respond(w, 0, nil, ErrInvalid)
			return
		}
		if view := r.URL.Query().Get("view"); view != "" && view != "summary" {
			respond(w, 0, nil, ErrInvalid)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func respondTask(w http.ResponseWriter, r *http.Request, status int, task *Task, err error) {
	if err == nil && r.URL.Query().Get("view") == "summary" {
		respond(w, status, summarize(task), nil)
		return
	}
	respond(w, status, task, err)
}

func pageParameters(r *http.Request, defaultLimit, minLimit, maxLimit int) (int, int, error) {
	parse := func(name string, fallback int) (int, error) {
		values, ok := r.URL.Query()[name]
		if !ok {
			return fallback, nil
		}
		if len(values) != 1 || values[0] == "" {
			return 0, ErrInvalid
		}
		value, err := strconv.Atoi(values[0])
		if err != nil || value < 0 {
			return 0, ErrInvalid
		}
		return value, nil
	}
	offset, err := parse("offset", 0)
	if err != nil {
		return 0, 0, err
	}
	limit, err := parse("limit", defaultLimit)
	if err != nil || limit < minLimit || limit > maxLimit {
		return 0, 0, ErrInvalid
	}
	return offset, limit, nil
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		respond(w, 0, nil, ErrInvalid)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		respond(w, 0, nil, ErrInvalid)
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		respond(w, 0, nil, ErrInvalid)
		return false
	}
	object := json.NewDecoder(bytes.NewReader(raw))
	object.DisallowUnknownFields()
	if err := object.Decode(out); err != nil {
		respond(w, 0, nil, ErrInvalid)
		return false
	}
	return true
}

func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		message := "internal workshop error"
		switch {
		case errors.Is(err, ErrNotFound):
			status, message = http.StatusNotFound, ErrNotFound.Error()
		case errors.Is(err, ErrInvalid), errors.Is(err, ErrWorkflow):
			status, message = http.StatusBadRequest, err.Error()
		case errors.Is(err, ErrConflict):
			status, message = http.StatusConflict, ErrConflict.Error()
		case errors.Is(err, ErrFull):
			status, message = http.StatusTooManyRequests, ErrFull.Error()
		case errors.Is(err, ErrClosed):
			status, message = http.StatusServiceUnavailable, ErrClosed.Error()
		default:
			status = http.StatusInternalServerError
		}
		value = map[string]string{"error": message}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requestNamespace(r *http.Request) string {
	namespace := r.URL.Query().Get("namespace")
	if namespace == "" {
		return "operator"
	}
	return namespace
}
