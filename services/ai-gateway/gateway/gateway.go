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
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
	cause   error
}

func (e *Error) Error() string        { return e.Code + ": " + e.Message }
func (e *Error) Unwrap() error        { return e.cause }
func fail(code, message string) error { return &Error{Code: code, Message: message} }
func caused(code, message string, cause error) error {
	return &Error{Code: code, Message: message, cause: cause}
}

func New(c Config) (*Gateway, error) {
	if len(c.Models) == 0 {
		return nil, fail("invalid_config", "at least one model is required")
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
		return nil, fail("invalid_config", "limits must be positive")
	}
	g.billing = c.Billing
	g.billingMaxOutput = c.BillingMaxOutputTokens
	if g.billingMaxOutput == 0 {
		g.billingMaxOutput = 4096
	}
	if g.billingMaxOutput < 1 || g.billingMaxOutput > 131072 {
		return nil, fail("invalid_config", "billing output cap must be 1..131072")
	}
	g.client = safeClient(c.HTTPClient)
	for alias, m := range c.Models {
		if alias == "" || m.Model == "" || !validEndpoint(m.Endpoint) {
			return nil, fail("invalid_config", "model alias, upstream model and full HTTP endpoint are required")
		}
		switch m.Protocol {
		case "chat_completions", "responses", "anthropic":
		case "custom":
			if err := validateMapping(m.Custom); err != nil {
				return nil, err
			}
		default:
			return nil, fail("invalid_config", "unknown protocol")
		}
		// Make configuration immutable even if the caller later edits its maps.
		b, err := json.Marshal(m)
		if err != nil {
			return nil, fail("invalid_config", "model configuration is not valid JSON")
		}
		var copy Model
		if json.Unmarshal(b, &copy) != nil {
			return nil, fail("invalid_config", "invalid model configuration")
		}
		copy.APIKey = m.APIKey
		m = copy
		for k, v := range m.Headers {
			if !validHeader(k, v) {
				return nil, fail("invalid_config", "invalid or reserved header")
			}
		}
		for src, dst := range m.ParameterMap {
			if src == "" || dst == "" || strings.Contains(dst, ".") || reserved(src) || reserved(dst) {
				return nil, fail("invalid_config", "parameter mapping targets a reserved or invalid field")
			}
		}
		if m.Price != nil {
			p := m.Price
			if p.Currency == "" {
				return nil, fail("invalid_config", "pricing requires currency")
			}
			for _, r := range []float64{p.InputPerMillion, p.OutputPerMillion, p.CacheReadPerMillion, p.CacheWritePerMillion} {
				if math.IsNaN(r) || math.IsInf(r, 0) || r < 0 {
					return nil, fail("invalid_config", "pricing rates must be finite and nonnegative")
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
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
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
	if obs.RequestID == "" {
		obs.RequestID = newRequestID()
	}
	defer func() {
		obs.Latency = time.Since(start)
		obs.Usage = out.Usage
		obs.Cost = out.Cost
		if err != nil {
			var e *Error
			if errors.As(err, &e) {
				obs.ErrorCode = e.Code
			} else {
				obs.ErrorCode = "internal_error"
			}
		}
		if settle != nil {
			if e := settle(obs); e != nil {
				obs.ErrorCode = "billing_unavailable"
				if err == nil {
					err = fail("billing_unavailable", "usage could not be durably recorded")
					out = ai.Response{}
				}
			}
		}
		if g.observer != nil {
			g.observer(obs)
		}
	}()
	if ctx.Err() != nil {
		return out, caused("canceled", "request canceled", ctx.Err())
	}
	m, ok := g.models[r.Model]
	if !ok {
		return out, fail("unknown_model", "model alias is not configured")
	}
	obs.Protocol = m.Protocol
	body, e := encodeRequest(m, r, emit != nil)
	if e != nil {
		return out, e
	}
	data, e := json.Marshal(body)
	if e != nil {
		return out, fail("invalid_request", "request is not valid JSON")
	}
	if int64(len(data)) > g.bodyLimit {
		return out, fail("request_too_large", "encoded request exceeds limit")
	}
	data, settle, e = g.admit(ctx, m, obs.RequestID, r.Model, "gateway.generate", data)
	if e != nil {
		return out, e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, m.Endpoint, bytes.NewReader(data))
	if e != nil {
		return out, fail("invalid_config", "invalid endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if emit != nil {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	for k, v := range m.Headers {
		req.Header.Set(k, v)
	}
	if m.Protocol == "anthropic" {
		if req.Header.Get("Anthropic-Version") == "" {
			req.Header.Set("Anthropic-Version", "2023-06-01")
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
			return out, caused("canceled", "request canceled", ctx.Err())
		}
		return out, caused("transport_error", "upstream request failed", e)
	}
	defer resp.Body.Close()
	obs.HTTPStatus = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, &Error{Code: "upstream_http_error", Message: "upstream rejected request", Status: resp.StatusCode}
	}
	if emit != nil {
		if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			return out, fail("invalid_response", "upstream did not return SSE")
		}
		wrapped := func(ev ai.Event) error {
			if !obs.HasFirstDelta {
				obs.FirstDelta = time.Since(start)
				obs.HasFirstDelta = true
			}
			if e := emit(ev); e != nil {
				return caused("callback_error", "stream callback failed", e)
			}
			return nil
		}
		out, e = decodeStream(resp.Body, m, r.Model, g.streamLimit, g.eventLimit, wrapped)
	} else {
		var raw []byte
		raw, e = readBounded(resp.Body, g.bodyLimit)
		if e == nil {
			out, e = decodeResponse(m, r.Model, raw)
		}
	}
	if ctx.Err() != nil {
		return ai.Response{}, caused("canceled", "request canceled", ctx.Err())
	}
	if e != nil {
		return ai.Response{}, e
	}
	out.Cost = calculateCost(out.Usage, m.Price)
	return out, nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e != nil {
		return nil, caused("read_error", "response read failed", e)
	}
	if int64(len(b)) > limit {
		return nil, fail("response_too_large", "response exceeds limit")
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
