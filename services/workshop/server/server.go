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
	return c, e
}

// New returns ownership of the durable service; close it after the HTTP server stops.
// An empty Listen defaults to :8443.
func New(c Config) (*http.Server, *workshop.Service, error) {
	if c.Listen == "" {
		c.Listen = ":8443"
	}
	// Validate TLS before opening or recovering the durable database.
	methods := map[string]rpc.Method{}
	server, e := rpc.NewServer(c.ServerConfig, methods, 1024*1024)
	if e != nil {
		return nil, nil, e
	}
	service, e := workshop.New(c.Workshop, nil)
	if e != nil {
		var pathError *workshop.RelaySocketPathError
		if errors.As(e, &pathError) {
			return nil, nil, pathError
		}
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
type messageParams struct {
	taskParams
	Text           string `json:"text"`
	IdempotencyKey string `json:"idempotency_key"`
}
type resumeParams struct {
	taskParams
	Input string `json:"input"`
}
type artifactParams struct {
	taskParams
	RunID string `json:"run_id,omitempty"`
	Path  string `json:"path"`
}
type eventsParams struct {
	taskParams
	After uint64 `json:"after,omitempty"`
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
		"workshop.workflows": rpc.Typed(func(ctx context.Context, _ namespaceParams, s *rpc.Stream) (any, *rpc.Error) {
			return service.Workflows(), nil
		}),
		"workshop.submit": rpc.Typed(func(ctx context.Context, p workshop.SubmitRequest, s *rpc.Stream) (any, *rpc.Error) {
			if strings.TrimSpace(p.IdempotencyKey) == "" {
				return nil, rpc.InvalidParams()
			}
			return summary(service.Submit(p))
		}),
		"workshop.get": rpc.Typed(func(ctx context.Context, p taskParams, s *rpc.Stream) (any, *rpc.Error) {
			if p.TaskID == "" {
				return nil, rpc.InvalidParams()
			}
			out, e := service.Summary(p.Namespace, p.TaskID)
			return out, domainError(e)
		}),
		"workshop.cancel": rpc.Typed(func(ctx context.Context, p taskParams, s *rpc.Stream) (any, *rpc.Error) {
			if p.TaskID == "" {
				return nil, rpc.InvalidParams()
			}
			return summary(service.Cancel(p.Namespace, p.TaskID))
		}),
		"workshop.evidence": func(ctx context.Context, raw json.RawMessage, stream *rpc.Stream) (any, *rpc.Error) {
			var p struct {
				taskParams
				RunID      string `json:"run_id,omitempty"`
				EvidenceID string `json:"evidence_id,omitempty"`
				Offset     int    `json:"offset,omitempty"`
				Limit      int    `json:"limit,omitempty"`
			}
			p.Limit = 8192
			if rpc.Decode(raw, &p) != nil || p.TaskID == "" {
				return nil, rpc.InvalidParams()
			}
			out, err := service.Evidence(p.Namespace, p.TaskID, p.RunID, p.EvidenceID, p.Offset, p.Limit)
			return out, domainError(err)
		},
		"workshop.message": rpc.Typed(func(ctx context.Context, p messageParams, stream *rpc.Stream) (any, *rpc.Error) {
			out, err := service.Message(p.Namespace, p.TaskID, p.Text, p.IdempotencyKey)
			return out, domainError(err)
		}),
		"workshop.resume": rpc.Typed(func(ctx context.Context, p resumeParams, s *rpc.Stream) (any, *rpc.Error) {
			if p.TaskID == "" {
				return nil, rpc.InvalidParams()
			}
			return summary(service.Resume(p.Namespace, p.TaskID, p.Input))
		}),
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
			out, e := service.ListPage(p.Namespace, p.Offset, p.Limit)
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
			out, e := service.Result(p.Namespace, p.TaskID, p.RunID, p.Offset, p.Limit)
			return out, domainError(e)
		},
		"workshop.artifact": rpc.Typed(func(ctx context.Context, p artifactParams, stream *rpc.Stream) (any, *rpc.Error) {
			if p.TaskID == "" || p.Path == "" {
				return nil, rpc.InvalidParams()
			}
			out, e := service.Artifact(p.Namespace, p.TaskID, p.RunID, p.Path)
			return out, domainError(e)
		}),
		"workshop.events": rpc.Typed(func(ctx context.Context, p eventsParams, s *rpc.Stream) (any, *rpc.Error) {
			if p.TaskID == "" {
				return nil, rpc.InvalidParams()
			}
			out, e := service.Events(p.Namespace, p.TaskID, p.After)
			if out == nil && e == nil {
				out = []workshop.Event{}
			}
			return out, domainError(e)
		}),
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
	case errors.Is(e, workshop.ErrDiskQuotaExceeded):
		out := rpc.Failure(-32014, "disk_quota_exceeded")
		var quota *workshop.DiskQuotaError
		if errors.As(e, &quota) {
			out.Message = quota.Error()
		} // Only counters/limits, never wrapper paths.
		return out
	case errors.Is(e, workshop.ErrDiskQuotaScanFailed):
		return rpc.Failure(-32015, "disk_quota_scan_failed")
	case errors.Is(e, workshop.ErrArtifactTooLarge):
		return rpc.Failure(-32013, "artifact_too_large")
	case errors.Is(e, workshop.ErrRunLimit):
		return rpc.Failure(-32009, "run_limit")
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
