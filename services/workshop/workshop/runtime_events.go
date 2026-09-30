package workshop

import (
	"easygo-agent/rpc"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
)

func (p *streamParser) parsePi(event nativeEvent) {
	switch event.Type {
	case "session":
		if _, err := uuid.Parse(event.ID); err != nil {
			p.fail(errors.New("invalid native session id"))
			return
		}
		p.result.SessionID = event.ID
		p.send(Event{Kind: EventSession, SessionID: event.ID})
	case "message_start":
		if event.Message.Role == "assistant" {
			p.piReady = false
		}
	case "message_end":
		if event.Message.Role != "assistant" {
			return
		}
		if event.Message.StopReason == "error" || event.Message.StopReason == "aborted" {
			p.fail(errors.New("engine reported failure"))
			return
		}
		p.piReady = event.Message.StopReason == "stop"
		text := ""
		for _, b := range event.Message.Content {
			if b.Type == "text" {
				text += b.Text
			}
		}
		p.result.Text = p.redact(text)
		p.result.Usage.InputTokens += event.Message.Usage.Input + event.Message.Usage.CacheRead
		p.result.Usage.OutputTokens += event.Message.Usage.Output
		p.result.Usage.CachedInputTokens += event.Message.Usage.CacheRead
		if text != "" {
			p.send(Event{Kind: EventText, Text: text})
		}
	case "agent_settled":
		if !p.piReady {
			p.fail(errors.New("pi settled without a successful final assistant message"))
			return
		}
		if p.success {
			p.fail(errors.New("duplicate engine terminal event"))
			return
		}
		p.success = true
		p.send(Event{Kind: EventResult, Text: p.result.Text, Usage: &p.result.Usage})
	}
}
func (p *streamParser) parseOpenClaw(raw []byte) {
	var out struct {
		OK       *bool           `json:"ok"`
		Error    json.RawMessage `json:"error"`
		Payloads []struct {
			Text    string `json:"text"`
			IsError bool   `json:"isError"`
		} `json:"payloads"`
		Meta struct {
			Aborted   bool            `json:"aborted"`
			Error     json.RawMessage `json:"error"`
			AgentMeta struct {
				SessionID string `json:"sessionId"`
				Usage     struct {
					Input     int64 `json:"input"`
					Output    int64 `json:"output"`
					CacheRead int64 `json:"cacheRead"`
				} `json:"usage"`
			} `json:"agentMeta"`
		} `json:"meta"`
	}
	// Native result envelopes contain additional metadata; validate duplicate keys
	// first, then decode the subset that proves successful task completion.
	var generic map[string]json.RawMessage
	if rpc.Decode(raw, &generic) != nil || json.Unmarshal(raw, &out) != nil || out.OK != nil && !*out.OK || (len(out.Error) != 0 && string(out.Error) != "null") || out.Meta.Aborted || out.Payloads == nil {
		p.fail(errors.New("invalid or failed OpenClaw result"))
		return
	}
	if len(out.Meta.Error) > 0 && string(out.Meta.Error) != "null" {
		p.fail(errors.New("OpenClaw reported failure"))
		return
	}
	for _, payload := range out.Payloads {
		if payload.IsError {
			p.fail(errors.New("OpenClaw reported error payload"))
			return
		}
	}
	id := out.Meta.AgentMeta.SessionID
	if _, err := uuid.Parse(id); err != nil {
		p.fail(errors.New("invalid native session id"))
		return
	}
	p.result.SessionID = id
	for _, payload := range out.Payloads {
		p.result.Text += p.redact(payload.Text)
	}
	usage := out.Meta.AgentMeta.Usage
	p.result.Usage = Usage{InputTokens: usage.Input + usage.CacheRead, OutputTokens: usage.Output, CachedInputTokens: usage.CacheRead}
	p.success = true
	p.send(Event{Kind: EventSession, SessionID: id})
	p.send(Event{Kind: EventResult, Text: p.result.Text, Usage: &p.result.Usage})
}

func (p *streamParser) parsePiRaw(raw []byte) {
	var header struct {
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Message json.RawMessage `json:"message"`
	}
	var strict map[string]json.RawMessage
	if rpc.Decode(raw, &strict) != nil || json.Unmarshal(raw, &header) != nil || header.Type == "" {
		p.fail(errors.New("malformed Pi event"))
		return
	}
	event := nativeEvent{Type: header.Type, ID: header.ID}
	if len(header.Message) > 0 {
		var role struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(header.Message, &role) != nil {
			p.fail(errors.New("malformed Pi message"))
			return
		}
		if role.Role == "assistant" {
			if json.Unmarshal(header.Message, &event.Message) != nil {
				p.fail(errors.New("malformed Pi assistant message"))
				return
			}
		}
	}
	p.parsePi(event)
}
