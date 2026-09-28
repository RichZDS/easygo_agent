package rpc

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Audit contains only RPC metadata. PrincipalID comes from the pinned TLS leaf,
// never a header or request parameter. Status is the actual HTTP response status;
// ErrorCode also reports business/SSE failures carried by an HTTP 200 response.
type Audit struct {
	Kind        string        `json:"kind"`
	RequestID   string        `json:"request_id"`
	PrincipalID string        `json:"principal_id"`
	Method      string        `json:"method"`
	Namespace   string        `json:"namespace"`
	Duration    time.Duration `json:"duration"`
	Status      int           `json:"status"`
	ErrorCode   string        `json:"error_code,omitempty"`
}

// JSONLogger serializes complete records under a shared lock. Use the same
// logger for model observations and RPC audit records sharing an output writer.
// Callers pass metadata structs only; request/response bodies are never logged.
func JSONLogger(w io.Writer) func(any) {
	var mu sync.Mutex
	encoder := json.NewEncoder(w)
	return func(record any) { mu.Lock(); defer mu.Unlock(); _ = encoder.Encode(record) }
}
