package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"easygo-agent/rpc"
)

// Reservation contains only accounting identity/bounds, never prompts or keys.
type Reservation struct {
	Namespace    string `json:"namespace"`
	RequestID    string `json:"request_id"`
	Fingerprint  string `json:"fingerprint"`
	Model        string `json:"model"`
	Source       string `json:"source"`
	InputTokens  int64  `json:"reserve_input_tokens"`
	OutputTokens int64  `json:"reserve_output_tokens"`
}
type Billing interface {
	Reserve(context.Context, Reservation) (func(Observation) error, error)
}
type namespaceKey struct{}

func WithNamespace(ctx context.Context, namespace string) context.Context {
	return context.WithValue(ctx, namespaceKey{}, namespace)
}

// Managed routes impose a finite output cap before reserving a conservative
// input bound. Actual usage is still retained if it exceeds this estimate.
func (g *Gateway) admit(ctx context.Context, m Model, id, alias, source string, raw []byte) ([]byte, func(Observation) error, error) {
	if g.billing == nil {
		return raw, nil, nil
	}
	namespace, _ := ctx.Value(namespaceKey{}).(string)
	if !rpc.ValidNamespace(namespace) {
		return nil, nil, fail(CodeBillingIdentityRequired, "account namespace required")
	}
	if m.Protocol == "custom" {
		return nil, nil, fail(CodeUnsupportedCapability, "managed billing requires a standard bounded protocol")
	}
	var body map[string]json.RawMessage
	if rpc.Decode(raw, &body) != nil || body == nil {
		return nil, nil, fail(CodeInvalidRequest, "invalid billable request")
	}
	limit := g.billingMaxOutput
	field := map[string]string{"responses": "max_output_tokens", "anthropic": "max_tokens", "chat_completions": "max_completion_tokens"}[m.Protocol]
	if field == "" {
		return nil, nil, fail(CodeUnsupportedCapability, "unsupported billable protocol")
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if value, ok := body[key]; ok {
			var requested int
			if json.Unmarshal(value, &requested) != nil || requested <= 0 {
				return nil, nil, fail(CodeInvalidRequest, "invalid output token bound")
			}
			if requested < limit {
				limit = requested
			}
			delete(body, key)
		}
	}
	body[field], _ = json.Marshal(limit)
	bounded, e := json.Marshal(body)
	if e != nil {
		return nil, nil, fail(CodeInvalidRequest, "invalid bounded request")
	}
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s", namespace, alias, m.Protocol, bounded)))
	finish, e := g.billing.Reserve(ctx, Reservation{Namespace: namespace, RequestID: id, Fingerprint: hex.EncodeToString(fingerprint[:]), Model: alias, Source: source, InputTokens: int64(len(bounded)) + 1024, OutputTokens: int64(limit)})
	if e != nil {
		var public interface{ BillingCode() string }
		if errors.As(e, &public) {
			return nil, nil, fail(Code(public.BillingCode()), "model credit authorization failed")
		}
		return nil, nil, fail(CodeBillingUnavailable, "model credit authorization unavailable")
	}
	return bounded, finish, nil
}
