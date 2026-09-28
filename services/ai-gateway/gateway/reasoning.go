package gateway

import (
	"encoding/json"

	"easygo-agent/services/ai-gateway/ai"
)

type providerState struct {
	Protocol string          `json:"protocol"`
	Value    json.RawMessage `json:"value"`
}

func state(protocol string, value any) json.RawMessage {
	return rawJSON(providerState{Protocol: protocol, Value: rawJSON(value)})
}
func stateValue(b ai.Block) json.RawMessage {
	var s providerState
	_ = json.Unmarshal(b.ProviderState, &s)
	return s.Value
}
func validateState(b ai.Block, protocol string) error {
	if len(b.ProviderState) == 0 {
		return nil
	}
	var s providerState
	if json.Unmarshal(b.ProviderState, &s) != nil || s.Protocol == "" || len(s.Value) == 0 || string(s.Value) == "null" {
		return fail("invalid_request", "invalid provider state envelope")
	}
	if s.Protocol != protocol {
		return fail("unsupported_capability", "provider state belongs to another protocol")
	}
	if b.Type != "reasoning" {
		return fail("unsupported_capability", "provider state is only supported on reasoning blocks")
	}
	switch protocol {
	case "chat_completions":
		var text string
		if json.Unmarshal(s.Value, &text) != nil {
			return fail("invalid_request", "invalid chat reasoning state")
		}
	case "responses":
		o, e := decodeObject(s.Value)
		if e != nil || str(o["type"]) != "reasoning" {
			return fail("invalid_request", "invalid responses reasoning state")
		}
	case "anthropic":
		o, e := decodeObject(s.Value)
		if e != nil {
			return fail("invalid_request", "invalid anthropic reasoning state")
		}
		switch str(o["type"]) {
		case "thinking":
			if str(o["signature"]) == "" {
				return fail("invalid_request", "thinking state requires signature")
			}
		case "redacted_thinking":
			if str(o["data"]) == "" {
				return fail("invalid_request", "redacted thinking requires data")
			}
		default:
			return fail("invalid_request", "invalid anthropic reasoning state")
		}
	case "custom":
	default:
		return fail("unsupported_capability", "unsupported reasoning state")
	}
	return nil
}

// Unsigned, provider-neutral reasoning text is preserved as assistant text for
// protocols whose reasoning input requires opaque provider state. Never invent
// a signature or reasoning item id.
func reasoningText(b ai.Block) string {
	if len(b.ProviderState) > 0 {
		var s string
		if json.Unmarshal(stateValue(b), &s) == nil {
			return s
		}
	}
	return b.Text
}
