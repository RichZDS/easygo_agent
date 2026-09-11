// Package gateway exposes the same conversation runtime used by the CLI.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/conversation"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type Handler struct {
	store  conversation.Store
	memory conversation.MemoryStore
	agent  adk.TypedAgent[*schema.AgenticMessage]
	queue  agentruntime.QueueManager
}

// New keeps the legacy synchronous mode when no manager is supplied. The
// application passes its shared QueueManager, which enables the durable 202
// protocol without changing the low-level handler tests and embedders that
// still use the direct runtime. Optional extras may be a MemoryStore and/or a
// QueueManager so both long-term memory injection and queued execution remain
// available on the same constructor.
func New(store conversation.Store, agent adk.TypedAgent[*schema.AgenticMessage], extras ...any) http.Handler {
	h := &Handler{store: store, agent: agent}
	for _, extra := range extras {
		switch v := extra.(type) {
		case conversation.MemoryStore:
			h.memory = v
		case agentruntime.QueueManager:
			h.queue = v
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /v1/users/{user}/sessions", h.create)
	mux.HandleFunc("GET /v1/users/{user}/sessions", h.list)
	mux.HandleFunc("GET /v1/users/{user}/sessions/{id}/messages", h.history)
	mux.HandleFunc("POST /v1/users/{user}/sessions/{id}/runs", h.run)
	mux.HandleFunc("GET /v1/users/{user}/sessions/{id}/runs", h.listRuns)
	mux.HandleFunc("GET /v1/users/{user}/sessions/{id}/runs/{run_id}", h.getRun)
	mux.HandleFunc("DELETE /v1/users/{user}/sessions/{id}/runs/{run_id}", h.cancelRun)
	mux.HandleFunc("GET /v1/users/{user}/sessions/{id}/runs/{run_id}/events", h.runEvents)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

// NewWithQueue is the explicit asynchronous constructor used by application
// bootstrap and embedders that share one QueueManager across modes.
func NewWithQueue(store conversation.QueueStore, manager agentruntime.QueueManager) http.Handler {
	return New(store, nil, manager)
}
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	s, err := h.store.Create(r.Context(), r.PathValue("user"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s)
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if err := conversation.ValidateUser(r.PathValue("user")); err != nil {
		writeError(w, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	sessions, err := h.store.List(r.Context(), r.PathValue("user"), limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sessions": sessions})
}
func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	if err := conversation.ValidateUser(r.PathValue("user")); err != nil {
		writeError(w, err)
		return
	}
	limit, _, err := pagination(r)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	after, err := strconv.ParseInt(defaultValue(r.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		writeJSON(w, 400, map[string]string{"error": "after must be a nonnegative turn ID"})
		return
	}
	turns, err := h.store.History(r.Context(), r.PathValue("user"), r.PathValue("id"), after, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	next := after
	if len(turns) > 0 {
		next = turns[len(turns)-1].ID
	}
	writeJSON(w, 200, map[string]any{"turns": turns, "next_after": next})
}
func (h *Handler) run(w http.ResponseWriter, r *http.Request) {
	if h.queue != nil {
		h.enqueueRun(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var input struct {
		Input string `json:"input"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeJSON(w, 400, map[string]string{"error": "expected JSON object with input (maximum 64 KiB)"})
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "expected one JSON object"})
		return
	}
	session := agentruntime.NewStored(r.Context(), h.agent, h.store, r.PathValue("user"), r.PathValue("id"), h.memory)
	run, err := session.StartContext(r.Context(), input.Input)
	if err != nil {
		writeError(w, err)
		return
	}
	defer run.Close()
	streaming := strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	if !streaming {
		terminal, consumeErr := agentruntime.Consume(run, nil)
		if consumeErr != nil {
			writeError(w, consumeErr)
			return
		}
		if terminal.Kind != agentruntime.EventCompleted {
			if terminal.Kind == agentruntime.EventFailed && terminal.Err != nil {
				writeError(w, terminal.Err)
				return
			}
			writeError(w, context.Canceled)
			return
		}
		writeJSON(w, 200, eventPayload(terminal))
		return
	}

	initialized := false
	terminal, consumeErr := agentruntime.Consume(run, func(event agentruntime.Event) error {
		if !initialized {
			if event.Kind == agentruntime.EventFailed {
				if event.Err != nil {
					return event.Err
				}
				return errors.New("agent run failed")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			initialized = true
		}
		controller := http.NewResponseController(w)
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		data, _ := json.Marshal(eventPayload(event))
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Kind, data); err != nil {
			return err
		}
		return controller.Flush()
	})
	if consumeErr != nil && !initialized {
		if terminal.Err != nil {
			writeError(w, terminal.Err)
		} else {
			writeError(w, consumeErr)
		}
	}
}

func (h *Handler) enqueueRun(w http.ResponseWriter, r *http.Request) {
	if err := conversation.ValidateUser(r.PathValue("user")); err != nil {
		writeError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var input struct {
		Input string `json:"input"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected JSON object with input (maximum 64 KiB)"})
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected one JSON object"})
		return
	}
	record, _, err := h.queue.Submit(r.Context(), r.PathValue("user"), r.PathValue("id"), input.Input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, record)
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	if h.queue == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "run queue is unavailable"})
		return
	}
	if err := conversation.ValidateUser(r.PathValue("user")); err != nil {
		writeError(w, err)
		return
	}
	record, err := h.queue.Get(r.Context(), r.PathValue("user"), r.PathValue("id"), r.PathValue("run_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	if h.queue == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "run queue is unavailable"})
		return
	}
	if err := conversation.ValidateUser(r.PathValue("user")); err != nil {
		writeError(w, err)
		return
	}
	limit, _, err := pagination(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	runs, err := h.queue.List(r.Context(), r.PathValue("user"), r.PathValue("id"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (h *Handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	if h.queue == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "run queue is unavailable"})
		return
	}
	if err := conversation.ValidateUser(r.PathValue("user")); err != nil {
		writeError(w, err)
		return
	}
	record, err := h.queue.Cancel(r.Context(), r.PathValue("user"), r.PathValue("id"), r.PathValue("run_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if record.Status == conversation.RunRunning {
		status = http.StatusAccepted
	}
	writeJSON(w, status, record)
}

func (h *Handler) runEvents(w http.ResponseWriter, r *http.Request) {
	if h.queue == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "run queue is unavailable"})
		return
	}
	if err := conversation.ValidateUser(r.PathValue("user")); err != nil {
		writeError(w, err)
		return
	}
	subscription, err := h.queue.Subscribe(r.Context(), r.PathValue("user"), r.PathValue("id"), r.PathValue("run_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer subscription.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	for {
		event, ok := <-subscription.Events()
		if !ok {
			return
		}
		data, marshalErr := json.Marshal(eventPayload(event))
		if marshalErr != nil {
			return
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Kind, data); err != nil {
			return
		}
		if err = controller.Flush(); err != nil {
			return
		}
		if event.IsTerminal() {
			return
		}
	}
}
func eventPayload(e agentruntime.Event) map[string]any {
	result := map[string]any{"kind": e.Kind}
	if e.RunID != "" {
		result["run_id"] = e.RunID
		result["position"] = e.Position
	}
	if e.Status != "" {
		result["status"] = e.Status
	}
	if e.Text != "" {
		result["text"] = e.Text
	}
	if e.Tool != "" {
		result["tool"] = e.Tool
	}
	if e.CallID != "" || e.Kind == agentruntime.EventToolStarted || e.Kind == agentruntime.EventToolFinished {
		result["call_id"] = e.CallID
	}
	if e.Arguments != "" || e.Kind == agentruntime.EventToolStarted {
		result["arguments"] = e.Arguments
	}
	if e.Result != "" || e.Kind == agentruntime.EventToolFinished {
		result["result"] = e.Result
	}
	if e.Err != nil {
		result["error"] = e.Err.Error()
	}
	return result
}
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "agent request failed"
	switch {
	case errors.Is(err, agentruntime.ErrQueueClosed), errors.Is(err, agentruntime.ErrStoreUnavailable):
		status = http.StatusServiceUnavailable
		message = err.Error()
	case errors.Is(err, conversation.ErrNotFound):
		status = 404
		message = err.Error()
	case errors.Is(err, conversation.ErrIdempotencyConflict):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, conversation.ErrQueueFull):
		status = http.StatusTooManyRequests
		message = err.Error()
	case errors.Is(err, conversation.ErrBusy), errors.Is(err, agentruntime.ErrRunInProgress):
		status = 409
		message = "conversation already running"
	case errors.Is(err, conversation.ErrInvalidUser), errors.Is(err, conversation.ErrEmptyInput), errors.Is(err, agentruntime.ErrEmptyInput):
		status = 400
		message = err.Error()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = 408
		message = "request canceled or timed out"
	}
	writeJSON(w, status, map[string]string{"error": message})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
func pagination(r *http.Request) (int, int, error) {
	limit, e1 := strconv.Atoi(defaultValue(r.URL.Query().Get("limit"), "50"))
	offset, e2 := strconv.Atoi(defaultValue(r.URL.Query().Get("offset"), "0"))
	if e1 != nil || e2 != nil || limit < 1 || limit > 100 || offset < 0 {
		return 0, 0, errors.New("limit must be 1..100; offset must be nonnegative")
	}
	return limit, offset, nil
}
