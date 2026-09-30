package gateway

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"easygo-agent/services/ai-gateway/ai"
)

var errStreamDone = errors.New("stream complete")

type limitedStream struct {
	r         io.Reader
	remaining int64
}

func (r *limitedStream) Read(b []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, fail(CodeResponseTooLarge, "stream exceeds limit")
	}
	if int64(len(b)) > r.remaining {
		b = b[:r.remaining]
	}
	n, e := r.r.Read(b)
	r.remaining -= int64(n)
	return n, e
}

// readSSE dispatches each event immediately; it bounds the total wire bytes,
// each line and each assembled event. EOF is never treated as completion.
func readSSE(r io.Reader, total int64, eventLimit int, handle func(string, []byte) error) error {
	s := bufio.NewScanner(&limitedStream{r: r, remaining: total})
	s.Buffer(make([]byte, min(4096, eventLimit)), eventLimit)
	var data strings.Builder
	event := ""
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if data.Len() > 0 {
				raw := strings.TrimSuffix(data.String(), "\n")
				e := handle(event, []byte(raw))
				if errors.Is(e, errStreamDone) {
					return nil
				}
				if e != nil {
					return e
				}
			}
			data.Reset()
			event = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			if data.Len()+len(value)+1 > eventLimit {
				return fail(CodeResponseTooLarge, "SSE event exceeds limit")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if e := s.Err(); e != nil {
		var ge *Error
		if errors.As(e, &ge) {
			return e
		}
		return caused(CodeStreamReadError, "SSE read failed or event exceeds limit", e)
	}
	return fail(CodeTruncatedStream, "stream ended without a terminal event")
}
func decodeStream(r io.Reader, m Model, alias string, total int64, eventLimit int, emit func(ai.Event) error) (ai.Response, error) {
	switch m.Protocol {
	case "chat_completions":
		return streamChat(r, alias, total, eventLimit, emit)
	case "responses":
		return streamResponses(r, alias, total, eventLimit, emit)
	case "anthropic":
		return streamAnthropic(r, alias, total, eventLimit, emit)
	}
	return ai.Response{}, fail(CodeUnsupportedCapability, "streaming is not supported")
}

type streamBlocks struct {
	blocks  []ai.Block
	indexes map[string]int
}

func (b *streamBlocks) block(key, typ string) (*ai.Block, int) {
	if b.indexes == nil {
		b.indexes = map[string]int{}
	}
	i, ok := b.indexes[key]
	if !ok {
		i = len(b.blocks)
		b.indexes[key] = i
		b.blocks = append(b.blocks, ai.Block{Type: typ})
	}
	return &b.blocks[i], i
}
func streamChat(r io.Reader, alias string, total int64, limit int, emit func(ai.Event) error) (ai.Response, error) {
	out := baseResponse(alias)
	var blocks streamBlocks
	finished := false
	err := readSSE(r, total, limit, func(event string, raw []byte) error {
		if event == "error" {
			return fail(CodeUpstreamError, "upstream stream failed")
		}
		if string(raw) == "[DONE]" {
			if !finished {
				return fail(CodeTruncatedStream, "chat stream has no finish reason")
			}
			if out.FinishReason == "tool_calls" && !hasToolCall(blocks.blocks) {
				return fail(CodeInvalidResponse, "tool finish without a tool call")
			}
			if err := validateBlocks(blocks.blocks); err != nil {
				return err
			}
			return errStreamDone
		}
		o, e := decodeObject(raw)
		if e != nil {
			return e
		}
		if e = providerError(o); e != nil {
			return e
		}
		if id := str(o["id"]); id != "" {
			out.ID = id
		}
		if o["usage"] != nil {
			out.Usage, e = parseUsage(o["usage"], "chat_completions")
			if e != nil {
				return e
			}
		}
		choices := arr(o["choices"])
		if len(choices) > 1 {
			return fail(CodeUnsupportedCapability, "multiple streaming choices are not supported")
		}
		for _, v := range choices {
			if finished {
				return fail(CodeInvalidResponse, "chat delta after finish")
			}
			c := obj(v)
			if n, ok := number(c["index"]); !ok || n != 0 {
				return fail(CodeInvalidResponse, "invalid choice index")
			}
			d := obj(c["delta"])
			if str(d["refusal"]) != "" {
				return fail(CodeRefusedResponse, "upstream refused the response")
			}
			for _, part := range []struct{ field, key, typ, event string }{{"content", "text", "text", "text_delta"}, {"reasoning_content", "reasoning", "reasoning", "reasoning_delta"}} {
				if v := d[part.field]; v != nil {
					s, ok := v.(string)
					if !ok {
						return fail(CodeInvalidResponse, "invalid text delta")
					}
					if s != "" || part.typ == "reasoning" {
						b, i := blocks.block(part.key, part.typ)
						b.Text += s
						if b.Type == "reasoning" {
							b.ProviderState = state("chat_completions", b.Text)
						}
						if e = emit(ai.Event{Type: part.event, Index: i, Delta: s}); e != nil {
							return e
						}
					}
				}
			}
			if v := d["tool_calls"]; v != nil {
				if _, ok := v.([]any); !ok {
					return fail(CodeInvalidResponse, "invalid tool call delta")
				}
			}
			for _, v := range arr(d["tool_calls"]) {
				t := obj(v)
				n, e := index(t["index"])
				if e != nil {
					return e
				}
				if typ := str(t["type"]); typ != "" && typ != "function" {
					return fail(CodeUnsupportedCapability, "unsupported streaming tool type")
				}
				b, i := blocks.block("tool:"+strconv.Itoa(n), "tool_call")
				f := obj(t["function"])
				id, name, args := str(t["id"]), str(f["name"]), str(f["arguments"])
				b.ID += id
				b.Name += name
				b.Arguments = append(b.Arguments, []byte(args)...)
				if e = emit(ai.Event{Type: "tool_call_delta", Index: i, ID: id, Name: name, Delta: args}); e != nil {
					return e
				}
			}
			if c["finish_reason"] != nil {
				out.FinishReason, e = finish(str(c["finish_reason"]))
				if e != nil {
					return e
				}
				finished = true
			}
		}
		return nil
	})
	out.Message.Content = blocks.blocks
	return out, err
}

func streamResponses(r io.Reader, alias string, total int64, limit int, emit func(ai.Event) error) (ai.Response, error) {
	out := baseResponse(alias)
	var blocks streamBlocks
	// Initial arguments may be a placeholder. Wait for done (or completed)
	// before emitting them, so ordinary argument deltas never double them.
	argumentsSent := map[int]bool{}
	argumentsDone := map[int]bool{}
	finishTool := func(n int, arguments string, item object) error {
		b, i := blocks.block("tool:"+strconv.Itoa(n), "tool_call")
		ev := ai.Event{Type: "tool_call_delta", Index: i}
		if b.ID == "" {
			b.ID = str(item["call_id"])
			ev.ID = b.ID
		}
		if b.Name == "" {
			b.Name = str(item["name"])
			ev.Name = b.Name
		}
		if arguments == "" {
			arguments = string(b.Arguments)
		}
		if !argumentsSent[n] && arguments != "" {
			if !json.Valid([]byte(arguments)) {
				return fail(CodeInvalidResponse, "invalid completed tool arguments")
			}
			b.Arguments = []byte(arguments)
			ev.Delta = arguments
			argumentsSent[n] = true
		}
		argumentsDone[n] = true
		if ev.ID != "" || ev.Name != "" || ev.Delta != "" {
			return emit(ev)
		}
		return nil
	}

	err := readSSE(r, total, limit, func(event string, raw []byte) error {
		o, e := decodeObject(raw)
		if e != nil {
			return e
		}
		if e = providerError(o); e != nil {
			return e
		}
		typ := str(o["type"])
		if typ == "" {
			typ = event
		}
		switch typ {
		case "error", "response.failed":
			return fail(CodeUpstreamError, "upstream stream failed")
		case "response.incomplete":
			return fail(CodeIncompleteResponse, "upstream response was incomplete")
		case "response.completed":
			out, e = decodeResponses(alias, obj(o["response"]))
			if e != nil {
				return e
			}
			for n, v := range arr(obj(o["response"])["output"]) {
				item := obj(v)
				if str(item["type"]) == "function_call" {
					if e := finishTool(n, str(item["arguments"]), item); e != nil {
						return e
					}
				}
			}

			return errStreamDone
		case "response.output_item.added":
			item := obj(o["item"])
			if str(item["type"]) == "reasoning" {
				n, e := index(o["output_index"])
				if e != nil {
					return e
				}
				blocks.block("reasoning:"+strconv.Itoa(n), "reasoning")
			}
			if str(item["type"]) == "function_call" {
				n, e := index(o["output_index"])
				if e != nil {
					return e
				}
				b, i := blocks.block("tool:"+strconv.Itoa(n), "tool_call")
				b.Arguments = []byte(str(item["arguments"]))
				b.ID = str(item["call_id"])
				b.Name = str(item["name"])
				return emit(ai.Event{Type: "tool_call_delta", Index: i, ID: b.ID, Name: b.Name})
			}
		case "response.function_call_arguments.done":
			n, e := index(o["output_index"])
			if e != nil {
				return e
			}
			return finishTool(n, str(o["arguments"]), nil)
		case "response.output_item.done":
			item := obj(o["item"])
			if str(item["type"]) == "function_call" {
				n, e := index(o["output_index"])
				if e != nil {
					return e
				}
				return finishTool(n, str(item["arguments"]), item)
			}

		case "response.function_call_arguments.delta":
			n, e := index(o["output_index"])
			if e != nil {
				return e
			}
			b, i := blocks.block("tool:"+strconv.Itoa(n), "tool_call")
			s, ok := o["delta"].(string)
			if !ok {
				return fail(CodeInvalidResponse, "invalid function argument delta")
			}
			if argumentsDone[n] {
				return fail(CodeInvalidResponse, "argument delta after tool completion")
			}
			if s != "" {
				if !argumentsSent[n] {
					b.Arguments = nil
				}
				argumentsSent[n] = true
			}

			b.Arguments = append(b.Arguments, []byte(s)...)
			return emit(ai.Event{Type: "tool_call_delta", Index: i, Delta: s})
		case "response.output_text.delta", "response.reasoning_summary_text.delta":
			n, e := index(o["output_index"])
			if e != nil {
				return e
			}
			idxField, blockType, eventType := "content_index", "text", "text_delta"
			if typ == "response.reasoning_summary_text.delta" {
				idxField, blockType, eventType = "summary_index", "reasoning", "reasoning_delta"
			}
			j, e := index(o[idxField])
			if e != nil {
				return e
			}
			s, ok := o["delta"].(string)
			if !ok {
				return fail(CodeInvalidResponse, "invalid text delta")
			}
			key := blockType + ":" + strconv.Itoa(n) + ":" + strconv.Itoa(j)
			if blockType == "reasoning" {
				key = "reasoning:" + strconv.Itoa(n)
			}
			b, i := blocks.block(key, blockType)
			b.Text += s
			return emit(ai.Event{Type: eventType, Index: i, Delta: s})
		case "response.refusal.delta":
			return fail(CodeRefusedResponse, "upstream refused the response")
		}
		return nil
	})
	return out, err
}

func streamAnthropic(r io.Reader, alias string, total int64, limit int, emit func(ai.Event) error) (ai.Response, error) {
	out := baseResponse(alias)
	started, finished := false, false
	usage := object{}
	open := map[int]bool{}
	partial := map[int]bool{}
	err := readSSE(r, total, limit, func(event string, raw []byte) error {
		o, e := decodeObject(raw)
		if e != nil {
			return e
		}
		if e = providerError(o); e != nil {
			return e
		}
		typ := str(o["type"])
		if typ == "" {
			typ = event
		}
		if typ == "ping" {
			return nil
		}
		if typ == "error" {
			return fail(CodeUpstreamError, "upstream stream failed")
		}
		switch typ {
		case "message_start":
			if started {
				return fail(CodeInvalidResponse, "duplicate message start")
			}
			started = true
			m := obj(o["message"])
			if m == nil {
				return fail(CodeInvalidResponse, "missing initial message")
			}
			out.ID = str(m["id"])
			for k, v := range obj(m["usage"]) {
				usage[k] = v
			}
		case "content_block_start":
			if !started || finished {
				return fail(CodeInvalidResponse, "content block outside message")
			}
			i, e := index(o["index"])
			if e != nil {
				return e
			}
			if i != len(out.Message.Content) {
				return fail(CodeInvalidResponse, "nonsequential content block")
			}
			b, e := anthropicBlock(obj(o["content_block"]))
			if e != nil {
				return e
			}
			out.Message.Content = append(out.Message.Content, b)
			open[i] = true
			switch b.Type {
			case "tool_call":
				return emit(ai.Event{Type: "tool_call_delta", Index: i, ID: b.ID, Name: b.Name})
			case "text", "reasoning":
				if b.Text != "" {
					typ := "text_delta"
					if b.Type == "reasoning" {
						typ = "reasoning_delta"
					}
					return emit(ai.Event{Type: typ, Index: i, Delta: b.Text})
				}
			}
		case "content_block_delta":
			i, e := index(o["index"])
			if e != nil {
				return e
			}
			if !open[i] || finished {
				return fail(CodeInvalidResponse, "delta outside open content block")
			}
			b := &out.Message.Content[i]
			d := obj(o["delta"])
			ev := ai.Event{Index: i}
			switch str(d["type"]) {
			case "text_delta":
				if b.Type != "text" {
					return fail(CodeInvalidResponse, "text delta block mismatch")
				}
				ev.Type = "text_delta"
				value, ok := d["text"].(string)
				if !ok {
					return fail(CodeInvalidResponse, "invalid content delta")
				}
				ev.Delta = value
				b.Text += ev.Delta
			case "thinking_delta":
				if b.Type != "reasoning" {
					return fail(CodeInvalidResponse, "thinking delta block mismatch")
				}
				ev.Type = "reasoning_delta"
				value, ok := d["thinking"].(string)
				if !ok {
					return fail(CodeInvalidResponse, "invalid content delta")
				}
				ev.Delta = value
				b.Text += ev.Delta
				reasoningState, _ := decodeObject(stateValue(*b))
				reasoningState["thinking"] = b.Text
				b.ProviderState = state("anthropic", reasoningState)
			case "input_json_delta":
				if b.Type != "tool_call" {
					return fail(CodeInvalidResponse, "argument delta block mismatch")
				}
				ev.Type = "tool_call_delta"
				value, ok := d["partial_json"].(string)
				if !ok {
					return fail(CodeInvalidResponse, "invalid content delta")
				}
				ev.Delta = value
				if !partial[i] && ev.Delta != "" {
					b.Arguments = nil
					partial[i] = true
				}
				b.Arguments = append(b.Arguments, []byte(ev.Delta)...)
			case "signature_delta":
				if b.Type != "reasoning" {
					return fail(CodeInvalidResponse, "signature delta block mismatch")
				}
				value, e := decodeObject(stateValue(*b))
				if e != nil {
					return e
				}
				signature, ok := d["signature"].(string)
				if !ok {
					return fail(CodeInvalidResponse, "invalid signature delta")
				}
				value["signature"] = str(value["signature"]) + signature
				b.ProviderState = state("anthropic", value)
				return nil
			default:
				return fail(CodeUnsupportedCapability, "unsupported content delta")
			}
			return emit(ev)
		case "content_block_stop":
			i, e := index(o["index"])
			if e != nil {
				return e
			}
			if !open[i] {
				return fail(CodeInvalidResponse, "content block stop without start")
			}
			delete(open, i)
			if e = validateState(out.Message.Content[i], "anthropic"); e != nil {
				return fail(CodeInvalidResponse, "incomplete anthropic continuation state")
			}
			if e = validateBlocks(out.Message.Content[i : i+1]); e != nil {
				return e
			}
			b := out.Message.Content[i]
			if b.Type == "tool_call" && !partial[i] {
				if e := emit(ai.Event{Type: "tool_call_delta", Index: i, Delta: string(b.Arguments)}); e != nil {
					return e
				}
			}

		case "message_delta":
			if !started || len(open) > 0 {
				return fail(CodeInvalidResponse, "message delta before content completion")
			}
			if reason := str(obj(o["delta"])["stop_reason"]); reason != "" {
				out.FinishReason, e = finish(reason)
				if e != nil {
					return e
				}
				finished = true
			}
			for k, v := range obj(o["usage"]) {
				usage[k] = v
			}
		case "message_stop":
			if !started || !finished || len(open) > 0 {
				return fail(CodeTruncatedStream, "message stopped without complete blocks and finish reason")
			}
			if out.FinishReason == "tool_calls" && !hasToolCall(out.Message.Content) {
				return fail(CodeInvalidResponse, "tool finish without a tool call")
			}
			out.Usage, e = parseUsage(usage, "anthropic")
			if e != nil {
				return e
			}
			return errStreamDone
		}
		return nil
	})
	return out, err
}
