package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"easygo-agent/services/ai-gateway/ai"
)

// GenerateRequest is the /v1/generate JSON wire envelope (flattened ai.Request).
type GenerateRequest struct {
	ai.Request
	Stream bool `json:"stream,omitempty"`
}
type HandlerConfig struct {
	BearerToken     string
	MaxRequestBytes int64
}

// NewHandler exposes /healthz, /v1/models and /v1/generate. Health is public;
// models and generation require the configured bearer token, if any.
func NewHandler(g *Gateway, c HandlerConfig) http.Handler {
	limit := c.MaxRequestBytes
	if limit <= 0 {
		limit = defaultBodyLimit
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path != "/healthz" && path != "/v1/models" && path != "/v1/generate" {
			writeError(w, 404, fail("not_found", "route not found"))
			return
		}
		method := http.MethodGet
		if path == "/v1/generate" {
			method = http.MethodPost
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeError(w, 405, fail("method_not_allowed", "method not allowed"))
			return
		}
		if path == "/healthz" {
			writeJSON(w, 200, object{"status": "ok"})
			return
		}
		if c.BearerToken != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+c.BearerToken)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, 401, fail("unauthorized", "invalid bearer token"))
			return
		}
		if path == "/v1/models" {
			writeJSON(w, 200, object{"models": g.Models()})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		defer r.Body.Close()
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var input GenerateRequest
		if e := d.Decode(&input); e != nil {
			var max *http.MaxBytesError
			if errors.As(e, &max) {
				writeError(w, 413, fail("request_too_large", "request exceeds limit"))
			} else {
				writeError(w, 400, fail("invalid_request", "invalid request JSON"))
			}
			return
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			writeError(w, 400, fail("invalid_request", "trailing request data"))
			return
		}
		if !input.Stream {
			out, e := g.Complete(r.Context(), input.Request, nil)
			if e != nil {
				writeError(w, errorStatus(e), e)
				return
			}
			writeJSON(w, 200, out)
			return
		}
		if _, ok := w.(http.Flusher); !ok {
			writeError(w, 500, fail("stream_unavailable", "response writer cannot stream"))
			return
		}
		began := false
		send := func(event string, value any) error {
			if !began {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("X-Accel-Buffering", "no")
				began = true
			}
			return writeSSE(w, event, value)
		}
		out, e := g.Complete(r.Context(), input.Request, func(ev ai.Event) error { return send("delta", ev) })
		if e != nil {
			if !began {
				writeError(w, errorStatus(e), e)
			} else {
				_ = send("error", publicError(e))
			}
			return
		}
		_ = send("response", out)
	})
}
func publicError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return &Error{Code: e.Code, Message: e.Message, Status: e.Status}
	}
	return &Error{Code: "internal_error", Message: "request failed"}
}
func errorStatus(err error) int {
	e := publicError(err)
	switch e.Code {
	case "invalid_request", "invalid_parameters", "unsupported_capability":
		return 400
	case "unknown_model":
		return 404
	case "request_too_large":
		return 413
	case "canceled":
		return 408
	default:
		return 502
	}
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, e error) {
	writeJSON(w, status, object{"error": publicError(e)})
}
func writeSSE(w http.ResponseWriter, event string, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); e != nil {
		return e
	}
	return http.NewResponseController(w).Flush()
}

type HTTPClientConfig struct {
	// Endpoint is the full /v1/generate URL, not a base URL.
	Endpoint         string
	BearerToken      string
	HTTPClient       *http.Client
	MaxResponseBytes int64
	MaxStreamBytes   int64
	MaxEventBytes    int
}
type HTTPClient struct {
	config HTTPClientConfig
	client *http.Client
}

var _ ai.Client = (*HTTPClient)(nil)

func NewHTTPClient(c HTTPClientConfig) (*HTTPClient, error) {
	if !validEndpoint(c.Endpoint) {
		return nil, fail("invalid_config", "full gateway endpoint is required")
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = defaultBodyLimit
	}
	if c.MaxStreamBytes == 0 {
		c.MaxStreamBytes = defaultStreamLimit
	}
	if c.MaxEventBytes == 0 {
		c.MaxEventBytes = defaultEventLimit
	}
	if c.MaxResponseBytes < 1 || c.MaxStreamBytes < 1 || c.MaxEventBytes < 1 {
		return nil, fail("invalid_config", "limits must be positive")
	}
	return &HTTPClient{config: c, client: safeClient(c.HTTPClient)}, nil
}
func (c *HTTPClient) Complete(ctx context.Context, r ai.Request, emit func(ai.Event) error) (out ai.Response, err error) {
	defer func() {
		if ctx.Err() != nil {
			out = ai.Response{}
			err = caused("canceled", "request canceled", ctx.Err())
		}
	}()
	data, e := json.Marshal(GenerateRequest{Request: r, Stream: emit != nil})
	if e != nil {
		return out, fail("invalid_request", "request is not valid JSON")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint, bytes.NewReader(data))
	if e != nil {
		return out, fail("invalid_config", "invalid gateway endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.config.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.BearerToken)
	}
	resp, e := c.client.Do(req)
	if e != nil {
		return out, caused("transport_error", "gateway request failed", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, e := readBounded(resp.Body, c.config.MaxResponseBytes)
		if e != nil {
			return out, e
		}
		var wire struct {
			Error *Error `json:"error"`
		}
		if json.Unmarshal(raw, &wire) == nil && wire.Error != nil {
			return out, sanitizedRemoteError(wire.Error)
		}
		return out, &Error{Code: "gateway_http_error", Message: "gateway rejected request", Status: resp.StatusCode}
	}
	if emit == nil {
		raw, e := readBounded(resp.Body, c.config.MaxResponseBytes)
		if e != nil {
			return out, e
		}
		if json.Unmarshal(raw, &out) != nil {
			return ai.Response{}, fail("invalid_response", "invalid gateway response")
		}
		if e = validateRemoteResponse(out); e != nil {
			return ai.Response{}, e
		}
		return out, nil
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return out, fail("invalid_response", "gateway did not return SSE")
	}
	e = readSSE(resp.Body, c.config.MaxStreamBytes, c.config.MaxEventBytes, func(event string, raw []byte) error {
		switch event {
		case "delta":
			var ev ai.Event
			if json.Unmarshal(raw, &ev) != nil {
				return fail("invalid_response", "invalid gateway delta")
			}
			switch ev.Type {
			case "text_delta", "reasoning_delta", "tool_call_delta":
			default:
				return fail("invalid_response", "unknown gateway delta type")
			}
			if e := emit(ev); e != nil {
				return caused("callback_error", "stream callback failed", e)
			}
			return nil
		case "response":
			if json.Unmarshal(raw, &out) != nil {
				return fail("invalid_response", "invalid gateway response")
			}
			if e := validateRemoteResponse(out); e != nil {
				return e
			}
			return errStreamDone
		case "error":
			var ge Error
			if json.Unmarshal(raw, &ge) != nil {
				return fail("invalid_response", "invalid gateway error")
			}
			return sanitizedRemoteError(&ge)
		default:
			return fail("invalid_response", "unknown gateway stream event")
		}
	})
	if e != nil {
		return ai.Response{}, e
	}
	return out, nil
}
func validateRemoteResponse(r ai.Response) error {
	if r.Model == "" || r.Message.Role != "assistant" || r.FinishReason == "" {
		return fail("invalid_response", "incomplete gateway response")
	}
	if _, e := finish(r.FinishReason); e != nil {
		return e
	}
	if !validUsage(r.Usage) {
		return fail("invalid_response", "invalid gateway usage")
	}
	return validateBlocks(r.Message.Content)
}
func sanitizedRemoteError(e *Error) error {
	// Never trust remote error strings: a proxy may echo prompts or credentials.
	codes := map[string]string{"unknown_model": "model alias is not configured", "invalid_request": "invalid request", "invalid_parameters": "invalid request parameters", "unsupported_capability": "unsupported capability", "upstream_http_error": "upstream rejected request", "upstream_error": "upstream reported an error", "invalid_response": "invalid response", "truncated_stream": "stream ended without a terminal event", "incomplete_response": "upstream response was incomplete", "refused_response": "upstream refused the response", "canceled": "request canceled", "unauthorized": "invalid bearer token", "request_too_large": "request exceeds limit", "response_too_large": "response exceeds limit", "transport_error": "upstream request failed", "stream_read_error": "stream read failed", "callback_error": "stream callback failed"}
	message, ok := codes[e.Code]
	if !ok {
		return &Error{Code: "gateway_error", Message: "gateway request failed", Status: e.Status}
	}
	return &Error{Code: e.Code, Message: message, Status: e.Status}
}
