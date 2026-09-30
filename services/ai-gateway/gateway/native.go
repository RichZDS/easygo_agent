package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"easygo-agent/rpc"
	"easygo-agent/services/ai-gateway/ai"
)

// A unary RPC carries base64 bytes; cap at 8MiB even if other gateway limits
// are larger, leaving room for the envelope in a 16MiB RPC response.
const nativeBodyLimit int64 = 8 << 20

// NativeResponse retains a framework's wire format. SSE is bounded and buffered;
// it is released only after the provider response has been read completely.
type NativeResponse struct {
	ContentType string `json:"content_type"`
	Body        []byte `json:"body"`
}

// Native only calls the configured model generation endpoint. It never accepts
// a caller URL, headers, credential, arbitrary HTTP method or model override.
func (g *Gateway) Native(ctx context.Context, alias, protocol, requestID string, raw json.RawMessage) (out NativeResponse, err error) {
	start := time.Now()
	obs := Observation{RequestID: requestID, Model: alias}
	if obs.RequestID == "" {
		obs.RequestID = newRequestID()
	}
	var settle func(Observation) error
	var accounting *accountingReader
	var price *Pricing
	defer func() {
		if e := g.settleObservation(&obs, start, accounting, price, settle, err); e != nil {
			err, out = e, NativeResponse{}
		}
	}()
	m, ok := g.models[alias]
	if !ok {
		return out, fail(CodeUnknownModel, "model alias is not configured")
	}
	obs.Protocol = m.Protocol
	price = m.Price
	if protocol != m.Protocol || protocol == "custom" {
		return out, fail(CodeUnsupportedCapability, "runtime protocol does not match model route")
	}
	var body map[string]json.RawMessage
	if int64(len(raw)) > g.bodyLimit || rpc.Decode(raw, &body) != nil || body == nil {
		return out, fail(CodeInvalidRequest, "invalid native request")
	}
	// Operator parameters remain authoritative. Native framework tool definitions
	// are intentionally retained instead of translated into the neutral tool API.
	defaults, e := parameters(m, ai.Request{})
	if e != nil {
		return out, e
	}
	for k, v := range defaults {
		body[k], _ = json.Marshal(v)
	}
	body["model"], _ = json.Marshal(m.Model)
	data, e := json.Marshal(body)
	if e != nil || int64(len(data)) > g.bodyLimit {
		return out, fail(CodeRequestTooLarge, "native request exceeds limit")
	}
	data, settle, e = g.admit(ctx, m, obs.RequestID, alias, "gateway.native", data)
	if e != nil {
		return out, e
	}
	limit := g.bodyLimit
	if limit > nativeBodyLimit {
		limit = nativeBodyLimit
	}
	var res *http.Response
	res, accounting, e = g.upstream(ctx, m, protocol, data, "application/json, text/event-stream", limit)
	if res != nil {
		defer res.Body.Close()
		obs.HTTPStatus = res.StatusCode
	}
	if e != nil {
		return out, e
	}
	raw, e = readBounded(accounting, limit)
	if e != nil {
		return out, e
	}
	contentType := strings.ToLower(strings.Split(res.Header.Get("Content-Type"), ";")[0])
	if e = validateNative(raw, contentType, protocol, g.eventLimit); e != nil {
		return out, e
	}
	if contentType == "text/event-stream" {
		_, e = decodeStream(bytes.NewReader(raw), m, alias, limit, g.eventLimit, func(ai.Event) error { return nil })
	} else if contentType == "application/json" {
		_, e = decodeResponse(m, alias, raw)
	} else {
		return out, fail(CodeInvalidResponse, "unsupported native response content type")
	}
	// Usage and cost come from the accounting reader; parsing here only validates.
	// Native framework-specific tools may exceed the neutral parser's vocabulary,
	// so unsupported_capability still forwards the original bytes.
	if e != nil {
		var problem *Error
		if !errors.As(e, &problem) || problem.Code != CodeUnsupportedCapability {
			return out, e
		}
	}
	if ctx.Err() != nil {
		return out, caused(CodeCanceled, "request canceled", ctx.Err())
	}
	return NativeResponse{ContentType: contentType, Body: raw}, nil
}

// Check transport completion independently from the neutral parser, which may
// legitimately not understand native framework tool kinds.
func validateNative(raw []byte, contentType, protocol string, eventLimit int) error {
	check := func(data []byte) (map[string]json.RawMessage, error) {
		var o map[string]json.RawMessage
		if rpc.Decode(data, &o) != nil || o == nil {
			return nil, fail(CodeInvalidResponse, "invalid native JSON")
		}
		if value, ok := o["error"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fail(CodeUpstreamError, "native provider error")
		}
		return o, nil
	}
	if contentType == "application/json" {
		_, e := check(raw)
		return e
	}
	if !bytes.HasSuffix(raw, []byte("\n\n")) && !bytes.HasSuffix(raw, []byte("\r\n\r\n")) {
		return fail(CodeTruncatedStream, "native SSE framing incomplete")
	}
	var data []string
	size := 0
	done := false
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if line != "" {
			size += len(line)
			if size > eventLimit {
				return fail(CodeResponseTooLarge, "native SSE frame exceeds limit")
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(line[5:], " "))
			}
			continue
		}
		if len(data) == 0 {
			size = 0
			continue
		}
		payload := strings.Join(data, "\n")
		data = nil
		size = 0
		if done {
			return fail(CodeInvalidResponse, "native SSE data after terminal")
		}
		if payload == "[DONE]" && protocol == "chat_completions" {
			done = true
			continue
		}
		o, e := check([]byte(payload))
		if e != nil {
			return e
		}
		var typ string
		json.Unmarshal(o["type"], &typ)
		if typ == "error" || typ == "response.failed" || typ == "response.incomplete" {
			return fail(CodeUpstreamError, "native provider failed")
		}
		if protocol == "responses" && typ == "response.completed" {
			response, e := check(o["response"])
			if e != nil {
				return e
			}
			var status string
			json.Unmarshal(response["status"], &status)
			if status != "completed" {
				return fail(CodeIncompleteResponse, "native response incomplete")
			}
			done = true
		}
		if protocol == "anthropic" && typ == "message_stop" {
			done = true
		}
	}
	if !done {
		return fail(CodeTruncatedStream, "native SSE has no terminal event")
	}
	return nil
}
