package gateway

import (
	"bytes"
	"io"
	"strings"

	"easygo-agent/services/ai-gateway/ai"
)

// accountingReader observes usage independently from assistant/tool decoding.
// It retains only a bounded unary body or one bounded SSE frame, never persists
// model content, and reports unknown unless both token counters are explicit.
type accountingReader struct {
	reader          io.Reader
	protocol        string
	stream          bool
	eventLimit      int
	bodyLimit       int64
	body            []byte
	pending         string
	discarding      bool
	usage           ai.Usage
	anthropic       object
	anthropicOutput bool
}

func newAccountingReader(reader io.Reader, protocol, contentType string, limit int64, eventLimit int) *accountingReader {
	return &accountingReader{reader: io.LimitReader(reader, limit+1), protocol: protocol, stream: strings.HasPrefix(strings.ToLower(contentType), "text/event-stream"), eventLimit: eventLimit, bodyLimit: limit, anthropic: object{}}
}
func (r *accountingReader) Read(p []byte) (int, error) {
	n, e := r.reader.Read(p)
	if n > 0 {
		if r.stream {
			r.feed(string(p[:n]))
		} else if int64(len(r.body)) <= r.bodyLimit {
			r.body = append(r.body, p[:n]...)
		}
	}
	return n, e
}
func (r *accountingReader) feed(chunk string) {
	r.pending = strings.ReplaceAll(r.pending+chunk, "\r\n", "\n")
	for {
		at := strings.Index(r.pending, "\n\n")
		if at < 0 {
			break
		}
		frame := r.pending[:at]
		r.pending = r.pending[at+2:]
		if r.discarding {
			r.discarding = false
			continue
		}
		if len(frame) > r.eventLimit {
			continue
		}
		data := []string{}
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(line[5:], " "))
			}
		}
		if len(data) > 0 {
			r.observe([]byte(strings.Join(data, "\n")), true)
		}
	}
	if len(r.pending) > r.eventLimit {
		r.pending = r.pending[len(r.pending)-2:]
		r.discarding = true
	}
}
func (r *accountingReader) observe(raw []byte, stream bool) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("[DONE]")) {
		return
	}
	o, e := decodeObject(raw)
	if e != nil {
		return
	}
	var value any
	switch r.protocol {
	case "responses":
		value = o["usage"]
		if nested := obj(o["response"]); nested != nil {
			value = nested["usage"]
		}
	case "chat_completions":
		value = o["usage"]
	case "anthropic":
		if !stream {
			value = o["usage"]
			break
		}
		typ := str(o["type"])
		var fragment object
		if typ == "message_start" {
			fragment = obj(obj(o["message"])["usage"])
		}
		if typ == "message_delta" {
			fragment = obj(o["usage"])
			if fragment["output_tokens"] != nil {
				r.anthropicOutput = true
			}
		}
		for k, v := range fragment {
			r.anthropic[k] = v
		}
		if r.anthropicOutput {
			value = r.anthropic
		}
	}
	if value == nil {
		return
	}
	usage, e := parseUsage(value, r.protocol)
	if e != nil {
		r.usage = ai.Usage{}
		return
	}
	if usage.Known {
		r.usage = usage
	}
}
func (r *accountingReader) Usage() ai.Usage {
	if !r.stream && int64(len(r.body)) <= r.bodyLimit {
		r.observe(r.body, false)
	}
	return r.usage
}
