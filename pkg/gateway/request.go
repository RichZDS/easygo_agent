package gateway

import (
	"encoding/json"
	"strings"

	"easygo-agent/pkg/ai"
)

type object = map[string]any

func reserved(k string) bool {
	switch strings.ToLower(k) {
	case "model", "messages", "input", "instructions", "system", "stream", "stream_options", "tools", "authorization", "api_key", "apikey", "x-api-key", "headers", "endpoint", "url", "request_id", "n", "background", "previous_response_id", "conversation":
		return true
	}
	return false
}
func parameters(m Model, r ai.Request) (object, error) {
	if r.MaxOutputTokens < 0 {
		return nil, fail("invalid_request", "max output tokens must be nonnegative")
	}
	explicit := map[string]json.RawMessage{}
	if r.MaxOutputTokens > 0 {
		explicit["max_output_tokens"] = rawJSON(r.MaxOutputTokens)
	}
	if r.Temperature != nil {
		v, e := json.Marshal(*r.Temperature)
		if e != nil {
			return nil, fail("invalid_request", "temperature must be finite")
		}
		explicit["temperature"] = v
	}
	if r.ToolChoice != "" {
		explicit["tool_choice"] = rawJSON(r.ToolChoice)
	}
	out := object{}
	for _, layer := range []map[string]json.RawMessage{m.Parameters, r.Parameters, explicit} {
		mapped := map[string]bool{}
		for key, raw := range layer {
			if reserved(key) {
				return nil, fail("invalid_parameters", "parameter targets a reserved field")
			}
			dst := key
			if d, ok := m.ParameterMap[key]; ok {
				dst = d
			} else if key == "max_output_tokens" {
				switch m.Protocol {
				case "chat_completions":
					dst = "max_completion_tokens"
				case "anthropic":
					dst = "max_tokens"
				}
			}
			if reserved(dst) {
				return nil, fail("invalid_parameters", "parameter mapping targets a reserved field")
			}
			if mapped[dst] {
				return nil, fail("invalid_parameters", "parameters map to the same field")
			}
			mapped[dst] = true
			var value any
			if json.Unmarshal(raw, &value) != nil {
				return nil, fail("invalid_parameters", "parameter is not valid JSON")
			}
			out[dst] = value
		}
	}
	return out, nil
}
func validateRequest(r ai.Request) error {
	if len(r.Messages) == 0 {
		return fail("invalid_request", "messages are required")
	}
	for _, m := range r.Messages {
		switch m.Role {
		case "system", "user", "assistant", "tool":
		default:
			return fail("unsupported_capability", "unsupported message role")
		}
		if len(m.Content) == 0 {
			return fail("invalid_request", "message content is required")
		}
		for _, b := range m.Content {
			switch b.Type {
			case "reasoning":
				if m.Role != "assistant" {
					return fail("invalid_request", "reasoning requires assistant role")
				}
			case "text":
				if m.Role == "tool" {
					return fail("invalid_request", "tool messages require tool_result blocks")
				}
			case "image":
				if m.Role != "user" || (b.URL == "" && (b.Data == "" || b.MediaType == "")) {
					return fail("unsupported_capability", "images require user role and URL or base64 data with media type")
				}
			case "tool_call":
				if m.Role != "assistant" || b.ID == "" || b.Name == "" || !json.Valid(b.Arguments) {
					return fail("invalid_request", "tool calls require assistant role, id, name and JSON arguments")
				}
			case "tool_result":
				if (m.Role != "tool" && m.Role != "user") || b.ID == "" {
					return fail("invalid_request", "tool results require tool or user role and call id")
				}
			default:
				return fail("unsupported_capability", "unsupported input block type")
			}
		}
	}
	names := map[string]bool{}
	for _, t := range r.Tools {
		if t.Name == "" || names[t.Name] || !json.Valid(t.Parameters) {
			return fail("invalid_request", "tools require unique names and valid JSON schemas")
		}
		names[t.Name] = true
	}
	return nil
}
func encodeRequest(m Model, r ai.Request, stream bool) (object, error) {
	if err := validateRequest(r); err != nil {
		return nil, err
	}
	for _, msg := range r.Messages {
		for _, b := range msg.Content {
			if err := validateState(b, m.Protocol); err != nil {
				return nil, err
			}
		}
	}
	if m.Protocol == "custom" {
		if stream {
			return nil, fail("unsupported_capability", "custom protocol does not support streaming")
		}
		return encodeCustom(m, r)
	}
	out, err := parameters(m, r)
	if err != nil {
		return nil, err
	}
	out["model"] = m.Model
	out["stream"] = stream
	if choice, ok := out["tool_choice"]; ok {
		s, ok := choice.(string)
		if !ok {
			return nil, fail("invalid_request", "tool_choice must be a string")
		}
		switch s {
		case "auto", "none", "required":
		default:
			return nil, fail("unsupported_capability", "tool_choice must be auto, none or required")
		}
		if m.Protocol == "anthropic" {
			if s == "required" {
				s = "any"
			}
			out["tool_choice"] = object{"type": s}
		}
	}
	tools := make([]any, 0, len(r.Tools))
	for _, t := range r.Tools {
		f := object{"name": t.Name, "description": t.Description, "parameters": t.Parameters}
		switch m.Protocol {
		case "chat_completions":
			tools = append(tools, object{"type": "function", "function": f})
		case "responses":
			f["type"] = "function"
			f["strict"] = false
			tools = append(tools, f)
		case "anthropic":
			tools = append(tools, object{"name": t.Name, "description": t.Description, "input_schema": t.Parameters})
		}
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	switch m.Protocol {
	case "chat_completions":
		out["messages"] = chatMessages(r.Messages)
		if stream {
			out["stream_options"] = object{"include_usage": true}
		}
	case "responses":
		out["input"] = responsesMessages(r.Messages)
	case "anthropic":
		messages, system := anthropicMessages(r.Messages)
		out["messages"] = messages
		if len(system) > 0 {
			out["system"] = system
		}
		if v, ok := out["max_tokens"]; !ok {
			return nil, fail("invalid_request", "anthropic requires max_output_tokens or a max_tokens default")
		} else if n, ok := v.(float64); !ok || n <= 0 || n != float64(int64(n)) {
			return nil, fail("invalid_request", "anthropic max_tokens must be a positive integer")
		}
	}
	return out, nil
}
func imageURL(b ai.Block) string {
	if b.URL != "" {
		return b.URL
	}
	return "data:" + b.MediaType + ";base64," + b.Data
}
func chatMessages(in []ai.Message) []any {
	out := []any{}
	for _, m := range in {
		parts := []any{}
		calls := []any{}
		reasoning := ""
		hasReasoning := false
		flush := func() {
			if len(parts) > 0 || len(calls) > 0 || hasReasoning {
				msg := object{"role": m.Role}
				if hasReasoning {
					msg["reasoning_content"] = reasoning
				}
				if len(parts) > 0 {
					msg["content"] = parts
				}
				if len(calls) > 0 {
					msg["tool_calls"] = calls
				}
				out = append(out, msg)
				parts = []any{}
				calls = []any{}
				reasoning = ""
				hasReasoning = false
			}
		}
		for _, b := range m.Content {
			switch b.Type {
			case "reasoning":
				hasReasoning = true
				reasoning += reasoningText(b)
			case "text":
				parts = append(parts, object{"type": "text", "text": b.Text})
			case "image":
				parts = append(parts, object{"type": "image_url", "image_url": object{"url": imageURL(b)}})
			case "tool_call":
				calls = append(calls, object{"id": b.ID, "type": "function", "function": object{"name": b.Name, "arguments": string(b.Arguments)}})
			case "tool_result":
				flush()
				out = append(out, object{"role": "tool", "tool_call_id": b.ID, "content": toolResultText(b)})
			}
		}
		flush()
	}
	return out
}

// Chat/Responses have no tool-result error flag; preserve it in the tool output.
func toolResultText(b ai.Block) string {
	if !b.IsError {
		return b.Text
	}
	raw, _ := json.Marshal(object{"is_error": true, "content": b.Text})
	return string(raw)
}
func responsesMessages(in []ai.Message) []any {
	out := []any{}
	for _, m := range in {
		parts := []any{}
		flush := func() {
			if len(parts) > 0 {
				out = append(out, object{"role": m.Role, "content": parts})
				parts = []any{}
			}
		}
		for _, b := range m.Content {
			switch b.Type {
			case "reasoning":
				if len(b.ProviderState) > 0 {
					flush()
					out = append(out, stateValue(b))
				} else if b.Text != "" {
					parts = append(parts, object{"type": "output_text", "text": b.Text})
				}
			case "text":
				typ := "input_text"
				if m.Role == "assistant" {
					typ = "output_text"
				}
				parts = append(parts, object{"type": typ, "text": b.Text})
			case "image":
				parts = append(parts, object{"type": "input_image", "image_url": imageURL(b)})
			case "tool_call":
				flush()
				out = append(out, object{"type": "function_call", "call_id": b.ID, "name": b.Name, "arguments": string(b.Arguments)})
			case "tool_result":
				flush()
				out = append(out, object{"type": "function_call_output", "call_id": b.ID, "output": toolResultText(b)})
			}
		}
		flush()
	}
	return out
}
func anthropicMessages(in []ai.Message) ([]any, []any) {
	out := []any{}
	system := []any{}
	for _, m := range in {
		parts := []any{}
		for _, b := range m.Content {
			switch b.Type {
			case "reasoning":
				if len(b.ProviderState) > 0 {
					parts = append(parts, stateValue(b))
				} else if b.Text != "" {
					parts = append(parts, object{"type": "text", "text": b.Text})
				}
			case "text":
				parts = append(parts, object{"type": "text", "text": b.Text})
			case "image":
				source := object{"type": "url", "url": b.URL}
				if b.URL == "" {
					source = object{"type": "base64", "media_type": b.MediaType, "data": b.Data}
				}
				parts = append(parts, object{"type": "image", "source": source})
			case "tool_call":
				parts = append(parts, object{"type": "tool_use", "id": b.ID, "name": b.Name, "input": b.Arguments})
			case "tool_result":
				parts = append(parts, object{"type": "tool_result", "tool_use_id": b.ID, "content": b.Text, "is_error": b.IsError})
			}
		}
		if m.Role == "system" {
			system = append(system, parts...)
			continue
		}
		role := m.Role
		if role == "tool" {
			role = "user"
		}
		out = append(out, object{"role": role, "content": parts})
	}
	return out, system
}
