package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"

	"easygo-agent/services/ai-gateway/ai"
)

func decodeObject(raw []byte) (object, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var o object
	if d.Decode(&o) != nil || o == nil {
		return nil, fail("invalid_response", "response must be a JSON object")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, fail("invalid_response", "response contains trailing JSON")
	}
	return o, nil
}
func obj(v any) object { m, _ := v.(map[string]any); return m }
func arr(v any) []any  { a, _ := v.([]any); return a }
func str(v any) string { s, _ := v.(string); return s }
func number(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, e := strconv.ParseInt(string(n), 10, 64)
	return i, e == nil && i >= 0
}
func index(v any) (int, error) {
	n, ok := number(v)
	if !ok || n > 100000 {
		return 0, fail("invalid_response", "invalid stream block index")
	}
	return int(n), nil
}
func rawJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func baseResponse(alias string) ai.Response {
	return ai.Response{Model: alias, Message: ai.Message{Role: "assistant", Content: []ai.Block{}}}
}
func providerError(o object) error {
	if o["error"] != nil {
		return fail("upstream_error", "upstream reported an error")
	}
	return nil
}

func decodeResponse(m Model, alias string, raw []byte) (ai.Response, error) {
	o, err := decodeObject(raw)
	if err != nil {
		return ai.Response{}, err
	}
	if err = providerError(o); err != nil {
		return ai.Response{}, err
	}
	switch m.Protocol {
	case "chat_completions":
		return decodeChat(alias, o)
	case "responses":
		return decodeResponses(alias, o)
	case "anthropic":
		return decodeAnthropic(alias, o)
	case "custom":
		return decodeCustom(m, alias, o)
	}
	return ai.Response{}, fail("invalid_config", "unknown protocol")
}
func finish(raw string) (string, error) {
	switch raw {
	case "stop", "end_turn", "stop_sequence", "completed":
		return "stop", nil
	case "tool_calls", "tool_use":
		return "tool_calls", nil
	case "length", "max_tokens", "incomplete", "model_context_window_exceeded":
		return "", fail("incomplete_response", "upstream output was truncated")
	case "content_filter", "refusal":
		return "", fail("refused_response", "upstream refused the response")
	default:
		return "", fail("unsupported_capability", "unsupported or missing finish reason")
	}
}
func validateBlocks(blocks []ai.Block) error {
	for _, b := range blocks {
		switch b.Type {
		case "text", "reasoning":
		case "tool_call":
			if b.ID == "" || b.Name == "" || !json.Valid(b.Arguments) {
				return fail("invalid_response", "upstream returned an incomplete tool call")
			}
		default:
			return fail("unsupported_capability", "unsupported output block")
		}
	}
	return nil
}
func decodeChat(alias string, o object) (ai.Response, error) {
	r := baseResponse(alias)
	r.ID = str(o["id"])
	choices := arr(o["choices"])
	if len(choices) != 1 {
		return r, fail("invalid_response", "exactly one choice is required")
	}
	choice := obj(choices[0])
	msg := obj(choice["message"])
	if msg == nil {
		return r, fail("invalid_response", "missing assistant message")
	}
	if msg["refusal"] != nil && str(msg["refusal"]) != "" {
		return r, fail("refused_response", "upstream refused the response")
	}
	if s, ok := msg["content"].(string); ok {
		r.Message.Content = append(r.Message.Content, ai.Block{Type: "text", Text: s})
	} else if msg["content"] != nil {
		return r, fail("unsupported_capability", "unsupported chat output content")
	}
	if s, ok := msg["reasoning_content"].(string); ok {
		r.Message.Content = append(r.Message.Content, ai.Block{Type: "reasoning", Text: s, ProviderState: state("chat_completions", s)})
	}
	if v := msg["tool_calls"]; v != nil {
		if _, ok := v.([]any); !ok {
			return r, fail("invalid_response", "invalid tool calls")
		}
	}
	for _, v := range arr(msg["tool_calls"]) {
		t := obj(v)
		if str(t["type"]) != "function" {
			return r, fail("unsupported_capability", "unsupported chat tool type")
		}
		f := obj(t["function"])
		r.Message.Content = append(r.Message.Content, ai.Block{Type: "tool_call", ID: str(t["id"]), Name: str(f["name"]), Arguments: json.RawMessage(str(f["arguments"]))})
	}
	var err error
	r.FinishReason, err = finish(str(choice["finish_reason"]))
	if err != nil {
		return r, err
	}
	if err = validateBlocks(r.Message.Content); err != nil {
		return r, err
	}
	if r.FinishReason == "tool_calls" && !hasToolCall(r.Message.Content) {
		return r, fail("invalid_response", "tool finish without a tool call")
	}
	r.Usage, err = parseUsage(o["usage"], "chat_completions")
	return r, err
}
func decodeResponses(alias string, o object) (ai.Response, error) {
	r := baseResponse(alias)
	r.ID = str(o["id"])
	if str(o["status"]) != "completed" {
		return r, fail("incomplete_response", "upstream response did not complete")
	}
	output, ok := o["output"].([]any)
	if !ok {
		return r, fail("invalid_response", "missing response output")
	}
	r.FinishReason = "stop"
	for _, v := range output {
		item := obj(v)
		switch str(item["type"]) {
		case "message":
			content, ok := item["content"].([]any)
			if !ok {
				return r, fail("invalid_response", "invalid responses message content")
			}
			for _, c := range content {
				b := obj(c)
				switch str(b["type"]) {
				case "output_text":
					if _, ok := b["text"].(string); !ok {
						return r, fail("invalid_response", "invalid output text")
					}
					r.Message.Content = append(r.Message.Content, ai.Block{Type: "text", Text: str(b["text"])})
				case "refusal":
					return r, fail("refused_response", "upstream refused the response")
				default:
					return r, fail("unsupported_capability", "unsupported responses content")
				}
			}
		case "function_call":
			r.Message.Content = append(r.Message.Content, ai.Block{Type: "tool_call", ID: str(item["call_id"]), Name: str(item["name"]), Arguments: json.RawMessage(str(item["arguments"]))})
			r.FinishReason = "tool_calls"
		case "reasoning":
			text := ""
			for _, v := range arr(item["summary"]) {
				b := obj(v)
				if str(b["type"]) != "summary_text" {
					return r, fail("unsupported_capability", "unsupported reasoning summary")
				}
				text += str(b["text"])
			}
			r.Message.Content = append(r.Message.Content, ai.Block{Type: "reasoning", Text: text, ProviderState: state("responses", item)})
		default:
			return r, fail("unsupported_capability", "unsupported responses output item")
		}
	}
	if err := validateBlocks(r.Message.Content); err != nil {
		return r, err
	}
	var err error
	r.Usage, err = parseUsage(o["usage"], "responses")
	return r, err
}
func anthropicBlock(o object) (ai.Block, error) {
	switch str(o["type"]) {
	case "text":
		if _, ok := o["text"].(string); !ok {
			return ai.Block{}, fail("invalid_response", "invalid anthropic text")
		}
		return ai.Block{Type: "text", Text: str(o["text"])}, nil
	case "tool_use":
		return ai.Block{Type: "tool_call", ID: str(o["id"]), Name: str(o["name"]), Arguments: rawJSON(o["input"])}, nil
	case "thinking":
		return ai.Block{Type: "reasoning", Text: str(o["thinking"]), ProviderState: state("anthropic", o)}, nil
	case "redacted_thinking":
		return ai.Block{Type: "reasoning", ProviderState: state("anthropic", o)}, nil
	default:
		return ai.Block{}, fail("unsupported_capability", "unsupported anthropic output block")
	}
}
func decodeAnthropic(alias string, o object) (ai.Response, error) {
	r := baseResponse(alias)
	r.ID = str(o["id"])
	content, ok := o["content"].([]any)
	if !ok {
		return r, fail("invalid_response", "missing message content")
	}
	for _, v := range content {
		b, e := anthropicBlock(obj(v))
		if e != nil {
			return r, e
		}
		if e = validateState(b, "anthropic"); e != nil {
			return r, fail("invalid_response", "incomplete anthropic continuation state")
		}
		r.Message.Content = append(r.Message.Content, b)
	}
	var err error
	r.FinishReason, err = finish(str(o["stop_reason"]))
	if err != nil {
		return r, err
	}
	if err = validateBlocks(r.Message.Content); err != nil {
		return r, err
	}
	if r.FinishReason == "tool_calls" && !hasToolCall(r.Message.Content) {
		return r, fail("invalid_response", "tool finish without a tool call")
	}
	r.Usage, err = parseUsage(o["usage"], "anthropic")
	return r, err
}
func parseUsage(v any, protocol string) (ai.Usage, error) {
	u := ai.Usage{}
	if v == nil {
		return u, nil
	}
	o := obj(v)
	if o == nil {
		return u, fail("invalid_response", "invalid usage")
	}
	input, output := "input_tokens", "output_tokens"
	if protocol == "chat_completions" {
		input, output = "prompt_tokens", "completion_tokens"
	}
	i, iok := number(o[input])
	n, nok := number(o[output])
	if (o[input] != nil && !iok) || (o[output] != nil && !nok) {
		return u, fail("invalid_response", "invalid usage counters")
	}
	if !iok || !nok {
		return u, nil
	}
	u.Known = true
	u.InputTokens = i
	u.OutputTokens = n
	read, write := o["cache_read_input_tokens"], o["cache_creation_input_tokens"]
	if protocol == "chat_completions" {
		read = obj(o["prompt_tokens_details"])["cached_tokens"]
	} else if protocol == "responses" {
		read = obj(o["input_tokens_details"])["cached_tokens"]
	}
	for v, dst := range map[*int64]any{&u.CacheReadTokens: read, &u.CacheWriteTokens: write} {
		if dst != nil {
			val, ok := number(dst)
			if !ok {
				return ai.Usage{}, fail("invalid_response", "invalid cache usage")
			}
			*v = val
		}
	}
	if protocol == "anthropic" { // Anthropic input_tokens excludes cache reads and writes.
		const maxInt64 = int64(^uint64(0) >> 1)
		if u.CacheReadTokens > maxInt64-u.InputTokens || u.CacheWriteTokens > maxInt64-u.InputTokens-u.CacheReadTokens {
			return ai.Usage{}, fail("invalid_response", "usage overflow")
		}
		u.InputTokens += u.CacheReadTokens + u.CacheWriteTokens
	}
	if !validUsage(u) {
		return ai.Usage{}, fail("invalid_response", "inconsistent usage counters")
	}
	return u, nil
}

func hasToolCall(blocks []ai.Block) bool {
	for _, b := range blocks {
		if b.Type == "tool_call" {
			return true
		}
	}
	return false
}
