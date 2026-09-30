package rpc_test

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"easygo-agent/rpc"
	"easygo-agent/rpc/rpctest"
)

// startRPC serves methods over mTLS for a "caller" identity allowed test.echo
// and test.fail in tenant-a. A non-nil raw handler replaces the RPC handler
// and keeps the TLS configuration.
func startRPC(t *testing.T, methods map[string]rpc.Method, raw http.Handler) (string, *tls.Config) {
	t.Helper()
	p := rpctest.NewPKI(t)
	server := p.Issue("server", false)
	caller := p.Issue("caller", false)
	s, e := rpc.NewServer(rpc.ServerConfig{Listen: "127.0.0.1:0", TLS: server, Authorization: []rpc.Authorization{{ID: "caller", CertFile: caller.CertFile, Methods: []string{"test.echo", "test.fail"}, Namespaces: []string{"tenant-a"}}}}, methods, 0)
	if e != nil {
		t.Fatal(e)
	}
	if raw != nil {
		s.Handler = raw
	}
	ts := rpctest.Start(t, s)
	cfg, e := rpc.ClientTLS(caller, server.CertFile)
	if e != nil {
		t.Fatal(e)
	}
	return ts.URL + "/rpc", cfg
}

func TestClientCallRoundTripAndServerErrors(t *testing.T) {
	type echo struct {
		Namespace string `json:"namespace"`
		N         int    `json:"n"`
	}
	var mu sync.Mutex
	var ids []string
	url, cfg := startRPC(t, map[string]rpc.Method{
		"test.echo": rpc.Typed(func(_ context.Context, p echo, s *rpc.Stream) (any, *rpc.Error) {
			mu.Lock()
			ids = append(ids, s.ID())
			mu.Unlock()
			return map[string]int{"n": p.N + 1}, nil
		}),
		"test.fail": func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) {
			return nil, rpc.Failure(-32002, "insufficient_credits")
		},
		"test.denied": func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return "unreachable", nil },
	}, nil)
	c, e := rpc.NewClient(url, cfg, rpc.Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		var out struct {
			N int `json:"n"`
		}
		if e := c.Call(ctx, "test.echo", echo{Namespace: "tenant-a", N: 41}, &out); e != nil || out.N != 42 {
			t.Fatalf("call %d: %+v %v", i, out, e)
		}
	}
	if e := c.Call(ctx, "test.echo", echo{Namespace: "tenant-a"}, nil); e != nil {
		t.Fatalf("nil out: %v", e)
	}
	mu.Lock()
	if len(ids) != 3 || ids[0] == ids[1] || ids[1] == ids[2] {
		t.Fatalf("request ids %v", ids)
	}
	for _, id := range ids {
		if b, e := hex.DecodeString(id); e != nil || len(b) != 16 {
			t.Fatalf("request id %q is not 16 random bytes in hex", id)
		}
	}
	mu.Unlock()
	for _, tc := range []struct {
		method string
		params any
		code   int
		data   string
	}{
		{"test.fail", echo{Namespace: "tenant-a"}, -32002, "insufficient_credits"},
		{"test.denied", echo{Namespace: "tenant-a"}, -32003, "forbidden"},
		{"test.echo", echo{Namespace: "tenant-b"}, -32003, "forbidden"},
		{"test.echo", map[string]any{"namespace": "tenant-a", "n": "x"}, -32602, "invalid_params"},
		{"test.missing", echo{Namespace: "tenant-a"}, -32601, "method_not_found"},
	} {
		e := c.Call(ctx, tc.method, tc.params, nil)
		var re *rpc.Error
		if !errors.As(e, &re) || re.Code != tc.code || re.Data.Code != tc.data {
			t.Errorf("%s %v => %v, want *Error %d %s", tc.method, tc.params, e, tc.code, tc.data)
		}
	}
}

func TestClientRejectsBadReplies(t *testing.T) {
	var mu sync.Mutex
	mode := "ok"
	url, cfg := startRPC(t, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		id, _ := json.Marshal(req.ID)
		// sized writes a valid reply of exactly n bytes.
		sized := func(n int) {
			head, tail := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"n":1,"pad":"`, id), `"}}`
			fmt.Fprint(w, head+strings.Repeat("x", n-len(head)-len(tail))+tail)
		}
		if r.URL.Path == "/followed" {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"n":1}}`, id)
			return
		}
		mu.Lock()
		m := mode
		mu.Unlock()
		switch m {
		case "ok":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"n":1}}`, id)
		case "wrong_id":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"other","result":{"n":1}}`)
		case "both":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{},"error":{"code":-32000,"message":"x","data":{"code":"x"}}}`, id)
		case "neither":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s}`, id)
		case "not_json":
			fmt.Fprint(w, "<html>")
		case "duplicate_key":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"n":1,"n":2}}`, id)
		case "extra_field":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"n":1},"extra":1}`, id)
		case "http_500":
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"n":1}}`, id)
		case "at_limit":
			sized(1024)
		case "too_large":
			sized(1025)
		case "bad_result":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"text"}`, id)
		case "redirect":
			http.Redirect(w, r, "https://"+r.Host+"/followed", http.StatusTemporaryRedirect)
		case "slow":
			time.Sleep(300 * time.Millisecond)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"n":1}}`, id)
		}
	}))
	c, e := rpc.NewClient(url, cfg, rpc.Options{Timeout: 150 * time.Millisecond, MaxResponseBytes: 1024})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	call := func(m string) error {
		mu.Lock()
		mode = m
		mu.Unlock()
		var out struct {
			N int `json:"n"`
		}
		return c.Call(context.Background(), "test.echo", map[string]string{"namespace": "tenant-a"}, &out)
	}
	for _, m := range []string{"ok", "at_limit"} {
		if e := call(m); e != nil {
			t.Fatalf("%s reply rejected: %v", m, e)
		}
	}
	for _, m := range []string{"wrong_id", "both", "neither", "not_json", "duplicate_key", "extra_field", "http_500", "too_large", "bad_result", "redirect", "slow"} {
		e := call(m)
		var re *rpc.Error
		if e == nil || errors.As(e, &re) {
			t.Errorf("%s => %v, want a non-RPC error", m, e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := c.Call(ctx, "test.echo", map[string]string{"namespace": "tenant-a"}, nil); e == nil {
		t.Fatal("canceled context accepted")
	}
}

func TestNewClientValidation(t *testing.T) {
	cfg := &tls.Config{}
	for _, u := range []string{"http://h/rpc", "https:///rpc", "https://u:p@h/rpc", "https://h/rpc?x=1", "https://h/rpc#f", "::"} {
		if _, e := rpc.NewClient(u, cfg, rpc.Options{}); e == nil {
			t.Errorf("accepted endpoint %q", u)
		}
	}
	for _, opts := range []rpc.Options{{Timeout: -1}, {MaxResponseBytes: -1}} {
		if _, e := rpc.NewClient("https://h/rpc", cfg, opts); e == nil {
			t.Errorf("accepted options %+v", opts)
		}
	}
	if _, e := rpc.NewClient("https://h/rpc", nil, rpc.Options{}); e == nil {
		t.Error("accepted a client without TLS")
	}
	c, e := rpc.NewClient("https://h/rpc", cfg, rpc.Options{})
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
}
