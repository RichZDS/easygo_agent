package rpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"time"
)

const DefaultMaxRequestBytes int64 = 8 * 1024 * 1024

var namespacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func ValidNamespace(s string) bool { return namespacePattern.MatchString(s) }

type ErrorData struct {
	Code string `json:"code"`
}
type Error struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	Data    ErrorData `json:"data"`
}

func (e *Error) Error() string { return e.Message }
func Failure(code int, domain string) *Error {
	return &Error{Code: code, Message: "Request failed", Data: ErrorData{Code: domain}}
}
func InvalidParams() *Error { return Failure(-32602, "invalid_params") }

type Envelope struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// MarshalJSON keeps success and error envelopes mutually exclusive, including
// successful nil results. The struct remains usable with standard Unmarshal.
func (e Envelope) MarshalJSON() ([]byte, error) {
	if e.Error != nil {
		return json.Marshal(struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Error   *Error `json:"error"`
		}{e.JSONRPC, e.ID, e.Error})
	}
	return json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Result  any    `json:"result"`
	}{e.JSONRPC, e.ID, e.Result})
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type Method func(context.Context, json.RawMessage, *Stream) (any, *Error)

// Typed strictly decodes params into P before calling fn; params that Decode
// rejects answer InvalidParams without running fn.
func Typed[P any](fn func(context.Context, P, *Stream) (any, *Error)) Method {
	return func(ctx context.Context, raw json.RawMessage, s *Stream) (any, *Error) {
		var p P
		if Decode(raw, &p) != nil {
			return nil, InvalidParams()
		}
		return fn(ctx, p, s)
	}
}

type Stream struct {
	w        http.ResponseWriter
	id       string
	began    bool
	terminal bool
	err      error
}

func (s *Stream) send(event string, value any) error {
	if s.err != nil {
		return s.err
	}
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if !s.began {
		s.w.Header().Set("Content-Type", "text/event-stream")
		s.w.Header().Set("Cache-Control", "no-cache")
		s.w.Header().Set("X-Accel-Buffering", "no")
		s.began = true
	}
	// Bound stalled network writes while allowing long computations between events.
	controller := http.NewResponseController(s.w)
	_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, s.err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b)
	if s.err == nil {
		s.err = controller.Flush()
	}
	return s.err
}
func (s *Stream) Delta(event any) error {
	if s.terminal {
		return errors.New("stream already finished")
	}
	return s.send("delta", map[string]any{"jsonrpc": "2.0", "method": "gateway.delta", "params": map[string]any{"id": s.id, "event": event}})
}

// Result is the only successful terminal stream event; never call it on error.
func (s *Stream) Result(result any) error {
	if s.terminal {
		return errors.New("stream already finished")
	}
	s.terminal = true
	return s.send("result", Envelope{JSONRPC: "2.0", ID: s.id, Result: result})
}
func (s *Stream) ID() string { return s.id }
func allowed(values []string, value string) bool {
	for _, v := range values {
		if v == "*" || v == value {
			return true
		}
	}
	return false
}
func errorStatus(e *Error) int {
	if e != nil {
		switch e.Code {
		case -32003:
			return 403
		case -32700, -32600, -32602:
			return 400
		}
	}
	return 200
}
func write(w http.ResponseWriter, id any, result any, e *Error) error {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(errorStatus(e))
	return json.NewEncoder(w).Encode(Envelope{JSONRPC: "2.0", ID: id, Result: result, Error: e})
}
func handler(p permissions, methods map[string]Method, limit int64, observe func(Audit)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		began := time.Now()
		audit := Audit{Kind: "rpc", Status: 200}
		defer func() {
			if observe != nil {
				audit.Duration = time.Since(began)
				observe(audit)
			}
		}()
		respond := func(id any, result any, e *Error) {
			audit.Status = errorStatus(e)
			if e != nil {
				audit.ErrorCode = e.Data.Code
			}
			if err := write(w, id, result, e); err != nil && audit.ErrorCode == "" {
				audit.ErrorCode = "write_error"
			}
		}
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			respond(nil, nil, Failure(-32003, "forbidden"))
			return
		}
		auth, ok := p[sha256.Sum256(r.TLS.PeerCertificates[0].Raw)]
		if !ok {
			respond(nil, nil, Failure(-32003, "forbidden"))
			return
		}
		audit.PrincipalID = auth.ID
		if r.URL.Path == "/healthz" {
			audit.Method = "health"
			if !allowed(auth.Methods, "health") {
				respond(nil, nil, Failure(-32003, "forbidden"))
				return
			}
			if r.Method != http.MethodGet {
				audit.Status = 405
				audit.ErrorCode = "method_not_allowed"
				w.WriteHeader(405)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok"}`)
			return
		}
		if r.URL.Path != "/rpc" {
			audit.Status = 404
			audit.ErrorCode = "not_found"
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			audit.Status = 405
			audit.ErrorCode = "method_not_allowed"
			w.WriteHeader(405)
			return
		}
		media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || media != "application/json" {
			respond(nil, nil, Failure(-32600, "invalid_request"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		defer r.Body.Close()
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			respond(nil, nil, Failure(-32600, "request_too_large"))
			return
		}
		if !json.Valid(raw) {
			respond(nil, nil, Failure(-32700, "parse_error"))
			return
		}
		var req request
		if Decode(raw, &req) != nil {
			respond(nil, nil, Failure(-32600, "invalid_request"))
			return
		}
		var id string
		if json.Unmarshal(req.ID, &id) != nil || id == "" || len(id) > 128 {
			respond(nil, nil, Failure(-32600, "invalid_request"))
			return
		}
		audit.RequestID = id
		if req.JSONRPC != "2.0" || req.Method == "" || len(req.Params) == 0 || bytes.TrimSpace(req.Params)[0] != '{' {
			respond(id, nil, Failure(-32600, "invalid_request"))
			return
		}
		audit.Method = req.Method
		var params map[string]json.RawMessage
		_ = json.Unmarshal(req.Params, &params)
		var namespace string
		namespaceErr := json.Unmarshal(params["namespace"], &namespace)
		if namespaceErr == nil && ValidNamespace(namespace) {
			audit.Namespace = namespace
		}
		method, ok := methods[req.Method]
		if !ok {
			respond(id, nil, Failure(-32601, "method_not_found"))
			return
		}
		if !allowed(auth.Methods, req.Method) {
			respond(id, nil, Failure(-32003, "forbidden"))
			return
		}
		if namespaceErr != nil || !ValidNamespace(namespace) {
			respond(id, nil, InvalidParams())
			return
		}
		if !allowed(auth.Namespaces, namespace) {
			respond(id, nil, Failure(-32003, "forbidden"))
			return
		}
		stream := &Stream{w: w, id: id}
		result, rpcErr := method(r.Context(), req.Params, stream)
		if rpcErr != nil {
			audit.ErrorCode = rpcErr.Data.Code
		}
		if stream.err != nil && audit.ErrorCode == "" {
			audit.ErrorCode = "stream_write_error"
		}
		if stream.terminal {
			return
		}
		if stream.began {
			if rpcErr == nil {
				rpcErr = Failure(-32603, "missing_terminal")
			}
			audit.ErrorCode = rpcErr.Data.Code
			stream.terminal = true
			_ = stream.send("error", Envelope{JSONRPC: "2.0", ID: id, Error: rpcErr})
			return
		}
		respond(id, result, rpcErr)
	})
}

// NewServer always uses mTLS and enforces the same pinned identity in TLS and HTTP.
func NewServer(c ServerConfig, methods map[string]Method, maxRequestBytes int64) (*http.Server, error) {
	if _, _, e := net.SplitHostPort(c.Listen); e != nil {
		return nil, errors.New("invalid listen address")
	}
	if maxRequestBytes == 0 {
		maxRequestBytes = DefaultMaxRequestBytes
	}
	if maxRequestBytes < 1 {
		return nil, errors.New("invalid request limit")
	}
	p, e := authorize(c.Authorization)
	if e != nil {
		return nil, e
	}
	tlsConfig, e := serverTLS(c.TLS, p)
	if e != nil {
		return nil, e
	}
	return &http.Server{Addr: c.Listen, Handler: handler(p, methods, maxRequestBytes, c.Audit), TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}, nil
}

// Serve binds only TLS, cancels in-flight requests on shutdown, and joins serving.
func Serve(ctx context.Context, s *http.Server) error {
	serving, cancel := context.WithCancel(ctx)
	defer cancel()
	s.BaseContext = func(net.Listener) context.Context { return serving }
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServeTLS("", "") }()
	select {
	case e := <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return errors.New("RPC server failed")
	case <-ctx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	e := s.Shutdown(shutdown)
	if e != nil {
		_ = s.Close()
	}
	<-done
	return e
}
