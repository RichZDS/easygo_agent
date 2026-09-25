package gateway

import (
	"encoding/json"
	"strings"

	"easygo-agent/pkg/ai"
)

// CustomMapping maps canonical field names to dot-separated object paths.
// Request messages/tools and response content retain their canonical JSON shape.
// Paths address objects only (no array indexing, wildcards or expressions).
type CustomMapping struct {
	Request  map[string]string `json:"request"`
	Response map[string]string `json:"response"`
}

var requestFields = map[string]bool{"model": true, "messages": true, "tools": true, "max_output_tokens": true, "temperature": true, "tool_choice": true}
var responseFields = map[string]bool{"id": true, "text": true, "content": true, "finish_reason": true, "usage.input_tokens": true, "usage.output_tokens": true, "usage.cache_read_tokens": true, "usage.cache_write_tokens": true}

func validPath(p string) bool {
	if p == "" {
		return false
	}
	for _, s := range strings.Split(p, ".") {
		if s == "" {
			return false
		}
		for _, r := range s {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return false
			}
		}
	}
	return true
}
func validateMapping(c *CustomMapping) error {
	if c == nil || c.Request["model"] == "" || c.Request["messages"] == "" || c.Response["finish_reason"] == "" || (c.Response["text"] == "") == (c.Response["content"] == "") {
		return fail("invalid_config", "custom mapping requires model/messages, finish_reason and exactly one of text/content")
	}
	if (c.Response["usage.input_tokens"] == "") != (c.Response["usage.output_tokens"] == "") {
		return fail("invalid_config", "custom usage requires both input and output mappings")
	}
	if c.Response["usage.input_tokens"] == "" && (c.Response["usage.cache_read_tokens"] != "" || c.Response["usage.cache_write_tokens"] != "") {
		return fail("invalid_config", "custom cache usage requires input and output mappings")
	}
	for _, side := range []struct {
		paths   map[string]string
		allowed map[string]bool
	}{{c.Request, requestFields}, {c.Response, responseFields}} {
		paths := []string{}
		for field, path := range side.paths {
			if !side.allowed[field] || !validPath(path) {
				return fail("invalid_config", "custom mapping field or path is invalid")
			}
			for _, other := range paths {
				if path == other || strings.HasPrefix(path, other+".") || strings.HasPrefix(other, path+".") {
					return fail("invalid_config", "custom mapping paths overlap")
				}
			}
			paths = append(paths, path)
		}
	}
	return nil
}
func setPath(o object, path string, value any) error {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		if o[p] == nil {
			o[p] = object{}
		}
		child := obj(o[p])
		if child == nil {
			return fail("invalid_parameters", "custom parameter collides with mapped field")
		}
		o = child
	}
	last := parts[len(parts)-1]
	if _, ok := o[last]; ok {
		return fail("invalid_parameters", "custom parameter collides with mapped field")
	}
	o[last] = value
	return nil
}
func getPath(o object, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var v any = o
	for _, p := range parts {
		m := obj(v)
		var ok bool
		v, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return v, true
}
func encodeCustom(m Model, r ai.Request) (object, error) {
	// Canonical knobs use mapping paths, vendor extras use ParameterMap.
	extras := r
	extras.MaxOutputTokens = 0
	extras.Temperature = nil
	extras.ToolChoice = ""
	out, e := parameters(m, extras)
	if e != nil {
		return nil, e
	}
	canonical := object{"model": m.Model, "messages": r.Messages}
	if len(r.Tools) > 0 {
		canonical["tools"] = r.Tools
	}
	if r.MaxOutputTokens > 0 {
		canonical["max_output_tokens"] = r.MaxOutputTokens
	}
	if r.Temperature != nil {
		canonical["temperature"] = *r.Temperature
	}
	if r.ToolChoice != "" {
		canonical["tool_choice"] = r.ToolChoice
	}
	for field := range requestFields {
		if field == "model" || field == "messages" || field == "tools" {
			continue
		}
		dst := field
		if d := m.ParameterMap[field]; d != "" {
			dst = d
		}
		if v, ok := out[dst]; ok {
			if _, set := canonical[field]; !set {
				canonical[field] = v
			}
			delete(out, dst)
		}
	}
	for _, path := range m.Custom.Request {
		root := strings.Split(path, ".")[0]
		if _, ok := out[root]; ok {
			return nil, fail("invalid_parameters", "parameter overrides custom request mapping")
		}
	}
	for field, v := range canonical {
		path := m.Custom.Request[field]
		if path == "" {
			return nil, fail("unsupported_capability", "custom mapping does not support a requested field")
		}
		if e := setPath(out, path, v); e != nil {
			return nil, e
		}
	}
	return out, nil
}
func decodeCustom(m Model, alias string, o object) (ai.Response, error) {
	r := baseResponse(alias)
	values := object{}
	for field, path := range m.Custom.Response {
		v, ok := getPath(o, path)
		if !ok {
			return r, fail("invalid_response", "custom response is missing a mapped field")
		}
		values[field] = v
	}
	if v, ok := values["id"]; ok {
		var good bool
		r.ID, good = v.(string)
		if !good {
			return r, fail("invalid_response", "custom id must be a string")
		}
	}
	if v, ok := values["text"]; ok {
		s, good := v.(string)
		if !good {
			return r, fail("invalid_response", "custom text must be a string")
		}
		r.Message.Content = append(r.Message.Content, ai.Block{Type: "text", Text: s})
	} else {
		if _, ok := values["content"].([]any); !ok {
			return r, fail("invalid_response", "custom content must be an array")
		}
		if json.Unmarshal(rawJSON(values["content"]), &r.Message.Content) != nil {
			return r, fail("invalid_response", "custom content is invalid")
		}
	}
	var err error
	r.FinishReason, err = finish(str(values["finish_reason"]))
	if err != nil {
		return r, err
	}
	if err = validateBlocks(r.Message.Content); err != nil {
		return r, err
	}
	u := ai.Usage{}
	i, io := number(values["usage.input_tokens"])
	n, no := number(values["usage.output_tokens"])
	if (values["usage.input_tokens"] != nil && !io) || (values["usage.output_tokens"] != nil && !no) {
		return r, fail("invalid_response", "invalid custom usage counters")
	}
	if io && no {
		u.Known = true
		u.InputTokens = i
		u.OutputTokens = n
	}
	for field, dst := range map[string]*int64{"usage.cache_read_tokens": &u.CacheReadTokens, "usage.cache_write_tokens": &u.CacheWriteTokens} {
		if v, ok := values[field]; ok {
			n, good := number(v)
			if !good {
				return r, fail("invalid_response", "invalid custom cache usage")
			}
			*dst = n
		}
	}
	if !validUsage(u) {
		return r, fail("invalid_response", "inconsistent custom usage")
	}
	r.Usage = u
	return r, nil
}
