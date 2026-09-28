// Package server exposes bounded workshop views exclusively over authenticated RPC.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"easygo-agent/rpc"
	"easygo-agent/services/workshop/workshop"
)

type Config struct {
	rpc.ServerConfig
	Workshop workshop.Config `json:"workshop"`
}

func LoadConfig(r io.Reader) (Config, error) {
	var c Config
	e := rpc.ReadConfig(r, &c)
	if c.Listen == "" {
		c.Listen = ":8443"
	}
	if e != nil {
		return c, e
	}
	if c.Workshop.BearerTokenEnv != "" {
		return c, errors.New("RPC listener does not accept bearer configuration")
	}
	return c, nil
}

// New returns ownership of the durable service; close it after the HTTP server stops.
func New(c Config) (*http.Server, *workshop.Service, error) {
	if c.Listen == "" {
		c.Listen = ":8443"
	}
	if c.Workshop.BearerTokenEnv != "" {
		return nil, nil, errors.New("RPC listener does not accept bearer configuration")
	}
	// Validate TLS before opening or recovering the durable database.
	methods := map[string]rpc.Method{}
	server, e := rpc.NewServer(c.ServerConfig, methods, 1024*1024)
	if e != nil {
		return nil, nil, e
	}
	service, e := workshop.New(c.Workshop, nil)
	if e != nil {
		return nil, nil, errors.New("cannot initialize workshop")
	}
	for k, v := range Methods(service) {
		methods[k] = v
	}
	return server, service, nil
}

type namespaceParams struct {
	Namespace string `json:"namespace"`
}
type taskParams struct {
	Namespace string `json:"namespace"`
	TaskID    string `json:"task_id"`
}

func Methods(service *workshop.Service) map[string]rpc.Method {
	summary := func(task *workshop.Task, err error) (any, *rpc.Error) {
		if err != nil {
			return nil, domainError(err)
		}
		result, err := service.Summary(task.Namespace, task.ID)
		return result, domainError(err)
	}
	return map[string]rpc.Method{
		"workshop.workflows": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			var p namespaceParams
			if rpc.Decode(raw, &p) != nil {
				return nil, rpc.InvalidParams()
			}
			return service.Workflows(), nil
		},
		"workshop.submit": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			var p workshop.SubmitRequest
			if rpc.Decode(raw, &p) != nil || strings.TrimSpace(p.IdempotencyKey) == "" {
				return nil, rpc.InvalidParams()
			}
			return summary(service.Submit(p))
		},
		"workshop.get": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			var p taskParams
			if rpc.Decode(raw, &p) != nil || p.TaskID == "" {
				return nil, rpc.InvalidParams()
			}
			out, e := service.Summary(p.Namespace, p.TaskID)
			return out, domainError(e)
		},
		"workshop.cancel": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			var p taskParams
			if rpc.Decode(raw, &p) != nil || p.TaskID == "" {
				return nil, rpc.InvalidParams()
			}
			return summary(service.Cancel(p.Namespace, p.TaskID))
		},
		"workshop.resume": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			var p struct {
				taskParams
				Input string `json:"input"`
			}
			if rpc.Decode(raw, &p) != nil || p.TaskID == "" {
				return nil, rpc.InvalidParams()
			}
			return summary(service.Resume(p.Namespace, p.TaskID, p.Input))
		},
		"workshop.list": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			var p struct {
				Namespace string `json:"namespace"`
				Offset    int    `json:"offset,omitempty"`
				Limit     int    `json:"limit,omitempty"`
			}
			p.Limit = 20
			if rpc.Decode(raw, &p) != nil {
				return nil, rpc.InvalidParams()
			}
			limit := p.Limit
			out, e := service.ListPage(p.Namespace, p.Offset, limit)
			return out, domainError(e)
		},
		"workshop.result": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			var p struct {
				taskParams
				RunID  string `json:"run_id,omitempty"`
				Offset int    `json:"offset,omitempty"`
				Limit  int    `json:"limit,omitempty"`
			}
			p.Limit = 8192
			if rpc.Decode(raw, &p) != nil || p.TaskID == "" {
				return nil, rpc.InvalidParams()
			}
			limit := p.Limit
			out, e := service.Result(p.Namespace, p.TaskID, p.RunID, p.Offset, limit)
			return out, domainError(e)
		},
		"workshop.artifact": func(ctx context.Context, raw json.RawMessage, stream *rpc.Stream) (any, *rpc.Error) {
			var p struct {
				taskParams
				RunID string `json:"run_id,omitempty"`
				Path  string `json:"path"`
			}
			if rpc.Decode(raw, &p) != nil || p.TaskID == "" || p.Path == "" {
				return nil, rpc.InvalidParams()
			}
			out, e := service.Artifact(p.Namespace, p.TaskID, p.RunID, p.Path)
			return out, domainError(e)
		},
		"workshop.events": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			var p struct {
				taskParams
				After uint64 `json:"after,omitempty"`
			}
			if rpc.Decode(raw, &p) != nil || p.TaskID == "" {
				return nil, rpc.InvalidParams()
			}
			out, e := service.Events(p.Namespace, p.TaskID, p.After)
			if out == nil && e == nil {
				out = []workshop.Event{}
			}
			return out, domainError(e)
		},
	}
}
func domainError(e error) *rpc.Error {
	switch {
	case e == nil:
		return nil
	case errors.Is(e, workshop.ErrNotFound):
		return rpc.Failure(-32004, "not_found")
	case errors.Is(e, workshop.ErrWorkflow):
		return rpc.Failure(-32602, "unknown_workflow")
	case errors.Is(e, workshop.ErrInvalid):
		return rpc.InvalidParams()
	case errors.Is(e, workshop.ErrArtifactTooLarge):
		return rpc.Failure(-32013, "artifact_too_large")
	case errors.Is(e, workshop.ErrConflict):
		return rpc.Failure(-32009, "conflict")
	case errors.Is(e, workshop.ErrFull):
		return rpc.Failure(-32029, "capacity_exhausted")
	case errors.Is(e, workshop.ErrClosed):
		return rpc.Failure(-32000, "unavailable")
	default:
		return rpc.Failure(-32603, "internal_error")
	}
}
