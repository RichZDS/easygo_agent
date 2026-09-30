// Package gateway translates the provider-neutral ai protocol to HTTP model APIs.
package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"easygo-agent/services/ai-gateway/ai"
)

const defaultBodyLimit int64 = 16 << 20
const defaultStreamLimit int64 = 64 << 20
const defaultEventLimit = 1 << 20
const anthropicVersion = "2023-06-01"

type Pricing struct {
	Currency             string  `json:"currency"`
	InputPerMillion      float64 `json:"input_per_million"`
	OutputPerMillion     float64 `json:"output_per_million"`
	CacheReadPerMillion  float64 `json:"cache_read_per_million"`
	CacheWritePerMillion float64 `json:"cache_write_per_million"`
}

type Model struct {
	Protocol     string                     `json:"protocol"`
	Endpoint     string                     `json:"endpoint"`
	APIKey       string                     `json:"-"`
	Model        string                     `json:"model"`
	Headers      map[string]string          `json:"headers,omitempty"`
	Parameters   map[string]json.RawMessage `json:"parameters,omitempty"`
	ParameterMap map[string]string          `json:"parameter_map,omitempty"`
	Price        *Pricing                   `json:"price,omitempty"`
	Custom       *CustomMapping             `json:"custom,omitempty"`
}

type Config struct {
	Billing                Billing
	BillingMaxOutputTokens int
	Models                 map[string]Model
	HTTPClient             *http.Client
	Observer               Observer
	MaxResponseBytes       int64
	MaxStreamBytes         int64
	MaxEventBytes          int
}

// Observation deliberately excludes payloads, upstream error text and credentials.
// Observer is invoked exactly once per Complete, synchronously, and must be safe
// for concurrent calls. FirstDelta is zero unless HasFirstDelta is true.
type Observation struct {
	RequestID     string        `json:"request_id"`
	Model         string        `json:"model"`
	Protocol      string        `json:"protocol"`
	HTTPStatus    int           `json:"http_status"`
	Latency       time.Duration `json:"latency"`
	FirstDelta    time.Duration `json:"first_delta"`
	HasFirstDelta bool          `json:"has_first_delta"`
	Usage         ai.Usage      `json:"usage"`
	Cost          ai.Cost       `json:"cost"`
	ErrorCode     string        `json:"error_code,omitempty"`
}
type Observer func(Observation)

type Gateway struct {
	billing                Billing
	billingMaxOutput       int
	models                 map[string]Model
	client                 *http.Client
	observer               Observer
	bodyLimit, streamLimit int64
	eventLimit             int
}

var _ ai.Client = (*Gateway)(nil)

// Error never includes provider bodies, URLs, headers or user content. Cause is
// available through errors.Is/As but is not serialized or included by Error().
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
	cause   error
}

func (e *Error) Error() string             { return string(e.Code) + ": " + e.Message }
func (e *Error) Unwrap() error             { return e.cause }
func fail(code Code, message string) error { return &Error{Code: code, Message: message} }
func caused(code Code, message string, cause error) error {
	return &Error{Code: code, Message: message, cause: cause}
}

func New(c Config) (*Gateway, error) {
	if len(c.Models) == 0 {
		return nil, fail(CodeInvalidConfig, "at least one model is required")
	}
	g := &Gateway{models: make(map[string]Model), observer: c.Observer, bodyLimit: c.MaxResponseBytes, streamLimit: c.MaxStreamBytes, eventLimit: c.MaxEventBytes}
	if g.bodyLimit == 0 {
		g.bodyLimit = defaultBodyLimit
	}
	if g.streamLimit == 0 {
		g.streamLimit = defaultStreamLimit
	}
	if g.eventLimit == 0 {
		g.eventLimit = defaultEventLimit
	}
	if g.bodyLimit < 1 || g.streamLimit < 1 || g.eventLimit < 1 {
		return nil, fail(CodeInvalidConfig, "limits must be positive")
	}
	g.billing = c.Billing
	g.billingMaxOutput = c.BillingMaxOutputTokens
	if g.billingMaxOutput == 0 {
		g.billingMaxOutput = 4096
	}
	if g.billingMaxOutput < 1 || g.billingMaxOutput > 131072 {
		return nil, fail(CodeInvalidConfig, "billing output cap must be 1..131072")
	}
	g.client = safeClient(c.HTTPClient)
	for alias, m := range c.Models {
		if alias == "" || m.Model == "" || !validEndpoint(m.Endpoint) {
			return nil, fail(CodeInvalidConfig, "model alias, upstream model and full HTTP endpoint are required")
		}
		switch m.Protocol {
		case "chat_completions", "responses", "anthropic":
		case "custom":
			// Managed billing needs a standard bounded protocol; fail at startup
			// rather than on every request.
			if c.Billing != nil {
				return nil, fail(CodeInvalidConfig, "managed billing requires a standard bounded protocol")
			}
			if err := validateMapping(m.Custom); err != nil {
				return nil, err
			}
		default:
			return nil, fail(CodeInvalidConfig, "unknown protocol")
		}
		// Make configuration immutable even if the caller later edits its maps.
		b, err := json.Marshal(m)
		if err != nil {
			return nil, fail(CodeInvalidConfig, "model configuration is not valid JSON")
		}
		var copy Model
		if json.Unmarshal(b, &copy) != nil {
			return nil, fail(CodeInvalidConfig, "invalid model configuration")
		}
		copy.APIKey = m.APIKey
		m = copy
		for k, v := range m.Headers {
			if !validHeader(k, v) {
				return nil, fail(CodeInvalidConfig, "invalid or reserved header")
			}
		}
		for src, dst := range m.ParameterMap {
			if src == "" || dst == "" || strings.Contains(dst, ".") || reserved(src) || reserved(dst) {
				return nil, fail(CodeInvalidConfig, "parameter mapping targets a reserved or invalid field")
			}
		}
		if m.Price != nil {
			p := m.Price
			if p.Currency == "" {
				return nil, fail(CodeInvalidConfig, "pricing requires currency")
			}
			for _, r := range []float64{p.InputPerMillion, p.OutputPerMillion, p.CacheReadPerMillion, p.CacheWritePerMillion} {
				if math.IsNaN(r) || math.IsInf(r, 0) || r < 0 {
					return nil, fail(CodeInvalidConfig, "pricing rates must be finite and nonnegative")
				}
			}
		}
		if _, err := parameters(m, ai.Request{}); err != nil {
			return nil, err
		}
		g.models[alias] = m
	}
	return g, nil
}

func validEndpoint(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.Fragment == ""
}
func validHeader(k, v string) bool {
	if k == "" || strings.ContainsAny(v, "\r\n") {
		return false
	}
	for _, r := range k {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			return false
		}
	}
	switch strings.ToLower(k) {
	case "authorization", "x-api-key", "proxy-authorization", "host", "content-length", "transfer-encoding", "connection", "content-type", "accept":
		return false
	}
	return true
}
func safeClient(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{Timeout: 2 * time.Minute}
	}
	copy := *c
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}
func (g *Gateway) Models() []string {
	names := make([]string, 0, len(g.models))
	for n := range g.models {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("gw-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (g *Gateway) Complete(ctx context.Context, r ai.Request, emit func(ai.Event) error) (out ai.Response, err error) {
	start := time.Now()
	obs := Observation{RequestID: r.RequestID, Model: r.Model}
	var settle func(Observation) error
	var accounting *accountingReader
	var price *Pricing
	if obs.RequestID == "" {
		obs.RequestID = newRequestID()
	}
	defer func() {
		obs.Usage, obs.Cost = out.Usage, out.Cost
		if e := g.settleObservation(&obs, start, accounting, price, settle, err); e != nil {
			err, out = e, ai.Response{}
		}
	}()
	if ctx.Err() != nil {
		return out, caused(CodeCanceled, "request canceled", ctx.Err())
	}
	m, ok := g.models[r.Model]
	if !ok {
		return out, fail(CodeUnknownModel, "model alias is not configured")
	}
	obs.Protocol = m.Protocol
	price = m.Price
	body, e := encodeRequest(m, r, emit != nil)
	if e != nil {
		return out, e
	}
	data, e := json.Marshal(body)
	if e != nil {
		return out, fail(CodeInvalidRequest, "request is not valid JSON")
	}
	if int64(len(data)) > g.bodyLimit {
		return out, fail(CodeRequestTooLarge, "encoded request exceeds limit")
	}
	data, settle, e = g.admit(ctx, m, obs.RequestID, r.Model, "gateway.generate", data)
	if e != nil {
		return out, e
	}
	accept, limit := "application/json", g.bodyLimit
	if emit != nil {
		accept, limit = "text/event-stream", g.streamLimit
	}
	var resp *http.Response
	resp, accounting, e = g.upstream(ctx, m, m.Protocol, data, accept, limit)
	if resp != nil {
		defer resp.Body.Close()
		obs.HTTPStatus = resp.StatusCode
	}
	if e != nil {
		return out, e
	}
	var responseReader io.Reader = resp.Body
	if accounting != nil {
		responseReader = accounting
	}
	if emit != nil {
		if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			return out, fail(CodeInvalidResponse, "upstream did not return SSE")
		}
		wrapped := func(ev ai.Event) error {
			if !obs.HasFirstDelta {
				obs.FirstDelta = time.Since(start)
				obs.HasFirstDelta = true
			}
			if e := emit(ev); e != nil {
				return caused(CodeCallbackError, "stream callback failed", e)
			}
			return nil
		}
		out, e = decodeStream(responseReader, m, r.Model, g.streamLimit, g.eventLimit, wrapped)
	} else {
		var raw []byte
		raw, e = readBounded(responseReader, g.bodyLimit)
		if e == nil {
			out, e = decodeResponse(m, r.Model, raw)
		}
	}
	// Only semantic terminal failures may be followed by a final usage frame.
	// Callback cancellation/protocol errors must stop the upstream immediately.
	var semantic *Error
	if e != nil && accounting != nil && ctx.Err() == nil && errors.As(e, &semantic) && (semantic.Code == CodeIncompleteResponse || semantic.Code == CodeRefusedResponse) {
		_, _ = io.Copy(io.Discard, accounting)
	}
	if ctx.Err() != nil {
		return ai.Response{}, caused(CodeCanceled, "request canceled", ctx.Err())
	}
	if e != nil {
		return ai.Response{}, e
	}
	out.Cost = calculateCost(out.Usage, m.Price)
	return out, nil
}

// upstream posts data to the model's configured endpoint. For standard
// protocols it wraps the response body in an accounting reader bounded by limit;
// custom mappings report usage only through the decoded response. A non-2xx
// status is drained through accounting and returned as upstream_http_error
// together with resp. Callers record the status and close resp.Body whenever
// resp is non-nil.
func (g *Gateway) upstream(ctx context.Context, m Model, protocol string, data []byte, accept string, limit int64) (*http.Response, *accountingReader, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, m.Endpoint, bytes.NewReader(data))
	if e != nil {
		return nil, nil, fail(CodeInvalidConfig, "invalid endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	for k, v := range m.Headers {
		req.Header.Set(k, v)
	}
	if protocol == "anthropic" {
		if req.Header.Get("Anthropic-Version") == "" {
			req.Header.Set("Anthropic-Version", anthropicVersion)
		}
		if m.APIKey != "" {
			req.Header.Set("X-Api-Key", m.APIKey)
		}
	} else if m.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.APIKey)
	}
	resp, e := g.client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, nil, caused(CodeCanceled, "request canceled", ctx.Err())
		}
		return nil, nil, caused(CodeTransportError, "upstream request failed", e)
	}
	var accounting *accountingReader
	if protocol != "custom" {
		accounting = newAccountingReader(resp.Body, protocol, resp.Header.Get("Content-Type"), limit, g.eventLimit)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if accounting != nil {
			_, _ = io.Copy(io.Discard, accounting)
		}
		return resp, accounting, &Error{Code: CodeUpstreamHTTPError, Message: "upstream rejected request", Status: resp.StatusCode}
	}
	return resp, accounting, nil
}

// settleObservation completes obs when a call returns: latency, accounted usage
// and cost, the error code (internal_error for errors that are not *Error),
// billing settlement and the observer. It returns billing_unavailable only when
// settlement fails for an otherwise successful call; the caller must then
// discard its result.
func (g *Gateway) settleObservation(obs *Observation, start time.Time, accounting *accountingReader, price *Pricing, settle func(Observation) error, err error) error {
	obs.Latency = time.Since(start)
	if accounting != nil {
		obs.Usage = accounting.Usage()
		obs.Cost = calculateCost(obs.Usage, price)
	}
	if err != nil {
		var e *Error
		obs.ErrorCode = string(CodeInternal)
		if errors.As(err, &e) {
			obs.ErrorCode = string(e.Code)
		}
	}
	var unrecorded error
	if settle != nil {
		if e := settle(*obs); e != nil {
			obs.ErrorCode = string(CodeBillingUnavailable)
			if err == nil {
				unrecorded = fail(CodeBillingUnavailable, "usage could not be durably recorded")
			}
		}
	}
	if g.observer != nil {
		g.observer(*obs)
	}
	return unrecorded
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e != nil {
		return nil, caused(CodeReadError, "response read failed", e)
	}
	if int64(len(b)) > limit {
		return nil, fail(CodeResponseTooLarge, "response exceeds limit")
	}
	return b, nil
}
func calculateCost(u ai.Usage, p *Pricing) ai.Cost {
	if !u.Known || p == nil || !validUsage(u) {
		return ai.Cost{}
	}
	amount := (float64(u.InputTokens-u.CacheReadTokens-u.CacheWriteTokens)*p.InputPerMillion + float64(u.OutputTokens)*p.OutputPerMillion + float64(u.CacheReadTokens)*p.CacheReadPerMillion + float64(u.CacheWriteTokens)*p.CacheWritePerMillion) / 1e6
	if math.IsInf(amount, 0) || math.IsNaN(amount) {
		return ai.Cost{}
	}
	return ai.Cost{Known: true, Currency: p.Currency, Amount: amount}
}
func validUsage(u ai.Usage) bool {
	return u.InputTokens >= 0 && u.OutputTokens >= 0 && u.CacheReadTokens >= 0 && u.CacheWriteTokens >= 0 && u.CacheReadTokens <= u.InputTokens && u.CacheWriteTokens <= u.InputTokens-u.CacheReadTokens
}
