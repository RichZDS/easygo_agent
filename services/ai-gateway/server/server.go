// Package server exposes the model gateway exclusively over authenticated RPC.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"easygo-agent/rpc"
	"easygo-agent/services/ai-gateway/ai"
	"easygo-agent/services/ai-gateway/gateway"
	"easygo-agent/services/ai-gateway/meter"
)

type Config struct {
	Meter    *meter.Config    `json:"meter,omitempty"`
	Billing  gateway.Billing  `json:"-"`
	Observer gateway.Observer `json:"-"`
	rpc.ServerConfig
	gateway.FileConfig
}

func LoadConfig(r io.Reader) (Config, error) {
	var c Config
	e := rpc.ReadConfig(r, &c)
	if c.Listen == "" {
		c.Listen = ":8441"
	}
	if e != nil {
		return c, e
	}
	if c.BearerTokenEnv != "" {
		return c, errors.New("RPC listener does not accept bearer configuration")
	}
	return c, nil
}
func New(c Config) (*http.Server, error) {
	if c.Listen == "" {
		c.Listen = ":8441"
	}
	if c.BearerTokenEnv != "" {
		return nil, errors.New("RPC listener does not accept bearer configuration")
	}
	raw, e := json.Marshal(c.FileConfig)
	if e != nil {
		return nil, errors.New("invalid gateway configuration")
	}
	config, _, e := gateway.LoadConfig(bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	config.Observer = c.Observer
	if c.Meter != nil && c.Billing == nil {
		return nil, errors.New("configured meter lifecycle must be initialized")
	}
	config.Billing = c.Billing
	if c.Meter != nil {
		config.BillingMaxOutputTokens = c.Meter.MaxOutputTokens
	}
	g, e := gateway.New(config)
	if e != nil {
		return nil, e
	}
	return rpc.NewServer(c.ServerConfig, Methods(g), c.MaxRequestBytes)
}

type modelsParams struct {
	Namespace string `json:"namespace"`
}
type nativeParams struct {
	Namespace string          `json:"namespace"`
	Model     string          `json:"model"`
	Protocol  string          `json:"protocol"`
	Body      json.RawMessage `json:"body"`
}
type generateParams struct {
	Namespace string      `json:"namespace"`
	Request   *ai.Request `json:"request"`
	Stream    bool        `json:"stream,omitempty"`
}

func Methods(g *gateway.Gateway) map[string]rpc.Method {
	return map[string]rpc.Method{
		"gateway.models": rpc.Typed(func(ctx context.Context, _ modelsParams, s *rpc.Stream) (any, *rpc.Error) {
			return map[string]any{"models": g.Models()}, nil
		}),
		"gateway.native": rpc.Typed(func(ctx context.Context, p nativeParams, s *rpc.Stream) (any, *rpc.Error) {
			if p.Model == "" || len(p.Body) == 0 {
				return nil, rpc.InvalidParams()
			}
			result, err := g.Native(gateway.WithNamespace(ctx, p.Namespace), p.Model, p.Protocol, s.ID(), p.Body)
			if err != nil {
				return nil, domainError(err)
			}
			return result, nil
		}),
		"gateway.generate": rpc.Typed(func(ctx context.Context, p generateParams, s *rpc.Stream) (any, *rpc.Error) {
			if p.Request == nil {
				return nil, rpc.InvalidParams()
			}
			p.Request.RequestID = s.ID()
			var emit func(ai.Event) error
			if p.Stream {
				emit = func(event ai.Event) error { return s.Delta(event) }
			}
			result, e := g.Complete(gateway.WithNamespace(ctx, p.Namespace), *p.Request, emit)
			if e != nil {
				return nil, domainError(e)
			}
			if p.Stream {
				if e = s.Result(result); e != nil {
					return nil, rpc.Failure(-32000, "stream_write_error")
				}
				return nil, nil
			}
			return result, nil
		}),
	}
}
func domainError(err error) *rpc.Error {
	var e *gateway.Error
	if errors.As(err, &e) {
		switch e.Code {
		case "insufficient_credits":
			return rpc.Failure(-32002, e.Code)
		case "duplicate_request":
			return rpc.Failure(-32009, e.Code)
		case "billing_identity_required":
			return rpc.Failure(-32003, e.Code)
		case "billing_unavailable":
			return rpc.Failure(-32000, e.Code)
		case "invalid_request", "invalid_parameters", "unsupported_capability", "request_too_large":
			return rpc.Failure(-32602, e.Code)
		case "unknown_model":
			return rpc.Failure(-32004, e.Code)
		case "upstream_http_error", "upstream_error", "invalid_response", "truncated_stream", "incomplete_response", "refused_response", "canceled", "response_too_large", "transport_error", "stream_read_error", "callback_error":
			return rpc.Failure(-32000, e.Code)
		}
	}
	return rpc.Failure(-32603, "internal_error")
}
