package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"easygo-agent/rpc"
	"easygo-agent/services/ai-gateway/gateway"
)

// TestDomainErrorSnapshot pins the RPC error for every code the gateway and
// meter produce, including the codes that fall back to internal_error.
func TestDomainErrorSnapshot(t *testing.T) {
	gatewayError := func(code string) error {
		var e gateway.Error
		if err := json.Unmarshal([]byte(fmt.Sprintf(`{"code":%q,"message":"private detail"}`, code)), &e); err != nil {
			t.Fatal(err)
		}
		return &e
	}
	for _, tc := range []struct {
		code string
		rpc  int
		data string
	}{
		{"insufficient_credits", -32002, "insufficient_credits"},
		{"duplicate_request", -32009, "duplicate_request"},
		{"billing_identity_required", -32003, "billing_identity_required"},
		{"billing_unavailable", -32000, "billing_unavailable"},
		{"invalid_request", -32602, "invalid_request"},
		{"invalid_parameters", -32602, "invalid_parameters"},
		{"unsupported_capability", -32602, "unsupported_capability"},
		{"request_too_large", -32602, "request_too_large"},
		{"unknown_model", -32004, "unknown_model"},
		{"upstream_http_error", -32000, "upstream_http_error"},
		{"upstream_error", -32000, "upstream_error"},
		{"invalid_response", -32000, "invalid_response"},
		{"truncated_stream", -32000, "truncated_stream"},
		{"incomplete_response", -32000, "incomplete_response"},
		{"refused_response", -32000, "refused_response"},
		{"canceled", -32000, "canceled"},
		{"response_too_large", -32000, "response_too_large"},
		{"transport_error", -32000, "transport_error"},
		{"stream_read_error", -32000, "stream_read_error"},
		{"callback_error", -32000, "callback_error"},
		// Produced by the gateway but never mapped: RPC callers see internal_error.
		{"invalid_config", -32603, "internal_error"},
		{"missing_environment", -32603, "internal_error"},
		{"read_error", -32603, "internal_error"},
		{"not_a_gateway_code", -32603, "internal_error"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			want := &rpc.Error{Code: tc.rpc, Message: "Request failed", Data: rpc.ErrorData{Code: tc.data}}
			for _, err := range []error{gatewayError(tc.code), fmt.Errorf("wrapped: %w", gatewayError(tc.code))} {
				if got := domainError(err); *got != *want {
					t.Fatalf("domainError(%v) = %+v, want %+v", err, got, want)
				}
			}
		})
	}
	if got := domainError(errors.New("not a gateway error")); *got != (rpc.Error{Code: -32603, Message: "Request failed", Data: rpc.ErrorData{Code: "internal_error"}}) {
		t.Fatalf("non-gateway error = %+v", got)
	}
}
