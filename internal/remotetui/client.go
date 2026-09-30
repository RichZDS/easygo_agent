// Package remotetui adapts the authenticated public platform API to the existing TUI.
// It never constructs or executes a local agent.
package remotetui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	base   string
	origin string
	http   *http.Client
}
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("platform HTTP %d", e.Status) }
func NewClient(address string) (*Client, error) {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("platform URL must be an origin")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, errors.New("HTTPS required except loopback development")
	}
	jar, _ := cookiejar.New(nil)
	return &Client{base: strings.TrimSuffix(address, "/"), origin: u.Scheme + "://" + u.Host, http: &http.Client{Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("platform redirects prohibited") }}}, nil
}
func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.origin)
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &HTTPError{Status: res.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20+1))
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return errors.New("platform response too large")
	}
	if out == nil {
		return nil
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) == nil {
		if problem, ok := envelope["error"]; ok && string(problem) != "null" && (len(envelope) == 1 || envelope["jsonrpc"] != nil) {
			return errors.New("platform RPC rejected")
		}
		if result, ok := envelope["result"]; ok && (len(envelope) == 1 || envelope["jsonrpc"] != nil) {
			raw = result
		}
	}
	return json.Unmarshal(raw, out)
}
func (c *Client) Login(ctx context.Context, email, password string) error {
	return c.request(ctx, "POST", "/api/login", map[string]string{"email": email, "password": password}, nil)
}
func (c *Client) Logout(ctx context.Context) error {
	return c.request(ctx, "POST", "/api/logout", map[string]any{}, nil)
}
func (c *Client) RPC(ctx context.Context, method string, params map[string]any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	if _, ok := params["namespace"]; ok {
		return errors.New("namespace is bound by the authenticated server")
	}
	return c.request(ctx, "POST", "/api/rpc", map[string]any{"method": method, "params": params}, out)
}

type Session struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
}

func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var page struct {
		Sessions []Session `json:"sessions"`
	}
	err := c.RPC(ctx, "agent.session.list", map[string]any{"limit": 100}, &page)
	return page.Sessions, err
}
func (c *Client) CreateSession(ctx context.Context) (Session, error) {
	var s Session
	err := c.RPC(ctx, "agent.session.create", nil, &s)
	return s, err
}

type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Arguments json.RawMessage `json:"arguments"`
}
type Message struct {
	Seq     int64   `json:"seq"`
	RunID   string  `json:"run_id"`
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}
type History struct {
	Messages      []Message `json:"messages"`
	Runs          []WireRun `json:"runs"`
	RunsTruncated bool      `json:"runs_truncated"`
	NextAfter     *int64    `json:"next_after"`
}

func (c *Client) History(ctx context.Context, session string, after int64) (History, error) {
	var h History
	err := c.RPC(ctx, "agent.session.history", map[string]any{"session_id": session, "after": after, "limit": 1000}, &h)
	return h, err
}
