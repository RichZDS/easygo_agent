// Package chatmodel adapts the application's Eino message seam to the gateway.
package chatmodel

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"easygo-agent/internal/config"
	"easygo-agent/services/ai-gateway/gateway"
	"github.com/cloudwego/eino/components/model"
)

// New routes every production model call through the provider-neutral gateway.
// Endpoint is a full URL. The legacy BaseURL shorthand appends the protocol path.
func New(_ context.Context, cfg config.ModelConfig) (model.AgenticModel, error) {
	protocol := cfg.Protocol
	if protocol == "" {
		protocol = "chat_completions"
	}
	if cfg.Streaming != nil && protocol != "easygo" {
		return nil, fmt.Errorf("model.streaming is only supported for protocol easygo")
	}
	if protocol == "easygo" {
		if err := cfg.ValidateRemoteGateway(); err != nil {
			return nil, err
		}
		remote, err := gateway.NewHTTPClient(gateway.HTTPClientConfig{Endpoint: cfg.Endpoint, BearerToken: cfg.APIKey, HTTPClient: &http.Client{Timeout: cfg.Timeout}})
		if err != nil {
			return nil, fmt.Errorf("create remote model gateway: %w", err)
		}
		adapter, err := NewAdapter(remote, cfg.Name)
		if err != nil {
			return nil, err
		}
		adapter.defaults = copyParameters(cfg.Parameters)
		adapter.streamingDisabled = cfg.Streaming != nil && !*cfg.Streaming
		return adapter, nil
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		base := strings.TrimRight(cfg.BaseURL, "/")
		if base == "" {
			if protocol == "anthropic" {
				base = "https://api.anthropic.com/v1"
			} else {
				base = "https://api.openai.com/v1"
			}
		}
		switch protocol {
		case "chat_completions":
			endpoint = base + "/chat/completions"
		case "responses":
			endpoint = base + "/responses"
		case "anthropic":
			endpoint = base + "/messages"
		case "custom":
			return nil, fmt.Errorf("custom protocol requires a full endpoint")
		default:
			return nil, fmt.Errorf("unsupported protocol %q", protocol)
		}
	}
	m := gateway.Model{Protocol: protocol, Endpoint: endpoint, APIKey: cfg.APIKey, Model: cfg.Name, Parameters: cfg.Parameters, ParameterMap: cfg.ParameterMap}
	if p := cfg.Pricing; p != nil {
		m.Price = &gateway.Pricing{Currency: p.Currency, InputPerMillion: p.InputPerMillion, OutputPerMillion: p.OutputPerMillion, CacheReadPerMillion: p.CacheReadPerMillion, CacheWritePerMillion: p.CacheWritePerMillion}
	}
	g, err := gateway.New(gateway.Config{Models: map[string]gateway.Model{cfg.Name: m}, HTTPClient: &http.Client{Timeout: cfg.Timeout}})
	if err != nil {
		return nil, fmt.Errorf("create model gateway: %w", err)
	}
	return NewAdapter(g, cfg.Name)
}
