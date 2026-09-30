package rpc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Options bound a Client's calls. Zero values select the defaults: a 30 second
// timeout per call and a 16 MiB reply, which fits the largest gateway.native
// result.
type Options struct {
	Timeout          time.Duration
	MaxResponseBytes int64
}

// Client calls one EasyGo JSON-RPC endpoint over mutual TLS. It never follows
// redirects and is safe for concurrent use.
type Client struct {
	url       string
	http      *http.Client
	transport *http.Transport
	limit     int64
}

// NewClient returns a client for the full endpoint URL, such as
// https://host:8443/rpc, using a TLS configuration from ClientTLS.
func NewClient(endpoint string, tlsConfig *tls.Config, opts Options) (*Client, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid RPC endpoint")
	}
	if tlsConfig == nil {
		return nil, errors.New("RPC client requires mutual TLS")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxResponseBytes == 0 {
		opts.MaxResponseBytes = 16 << 20
	}
	if opts.Timeout < 0 || opts.MaxResponseBytes < 0 {
		return nil, errors.New("invalid RPC client limits")
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, MaxIdleConnsPerHost: 4}
	client := &http.Client{Transport: transport, Timeout: opts.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{url: endpoint, http: client, transport: transport, limit: opts.MaxResponseBytes}, nil
}

// Call sends one request with a random 16-byte hex id and decodes the result
// into out with encoding/json, unless out is nil; pass a *json.RawMessage to
// apply stricter checks such as Decode. A JSON-RPC error from the server is
// returned unchanged as *Error whatever the HTTP status. Transport, limit and
// envelope failures are other errors.
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	var idBytes [16]byte
	if _, e := rand.Read(idBytes[:]); e != nil {
		return fmt.Errorf("RPC request id: %w", e)
	}
	id := hex.EncodeToString(idBytes[:])
	payload, e := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", id, method, params})
	if e != nil {
		return fmt.Errorf("encode RPC params: %w", e)
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if e != nil {
		return fmt.Errorf("build RPC request: %w", e)
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := c.http.Do(req)
	if e != nil {
		return fmt.Errorf("RPC transport: %w", e)
	}
	defer res.Body.Close()
	body, e := io.ReadAll(io.LimitReader(res.Body, c.limit+1))
	if e != nil || int64(len(body)) > c.limit {
		return errors.New("RPC response exceeds limit or cannot be read")
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *Error          `json:"error"`
	}
	if Decode(body, &envelope) != nil || envelope.JSONRPC != "2.0" || envelope.ID != id || (len(envelope.Result) > 0) == (envelope.Error != nil) {
		return errors.New("invalid RPC response envelope")
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("RPC HTTP status %d", res.StatusCode)
	}
	if out != nil && json.Unmarshal(envelope.Result, out) != nil {
		return errors.New("invalid RPC result")
	}
	return nil
}

// Close releases idle connections; calls in flight are unaffected.
func (c *Client) Close() { c.transport.CloseIdleConnections() }
