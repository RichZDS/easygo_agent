package rpc_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"easygo-agent/rpc"
	"easygo-agent/rpc/rpctest"
)

func TestRealTLSIdentityAndAuthorization(t *testing.T) {
	p := rpctest.NewPKI(t)
	server := p.Issue("server", false)
	client := p.Issue("client", false)
	unknown := p.Issue("unknown", false)
	expired := p.Issue("expired", true)
	outsider := rpctest.NewPKI(t).Issue("outsider", false)
	var calls atomic.Int64
	method := func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) {
		calls.Add(1)
		return map[string]bool{"ok": true}, nil
	}
	s, e := rpc.NewServer(rpc.ServerConfig{Listen: "127.0.0.1:0", TLS: server, Authorization: []rpc.Authorization{{ID: "client", CertFile: client.CertFile, Methods: []string{"health", "test.echo"}, Namespaces: []string{"tenant-a"}}, {ID: "expired", CertFile: expired.CertFile, Methods: []string{"health"}}}}, map[string]rpc.Method{"test.echo": method, "test.denied": method}, 0)
	if e != nil {
		t.Fatal(e)
	}
	ts := rpctest.Start(t, s)
	c := rpctest.Client(t, client, server.CertFile)
	status, out := rpctest.Call(t, c, ts.URL, "test.echo", `{"namespace":"tenant-a"}`)
	if status != 200 || out.Error != nil || calls.Load() != 1 {
		t.Fatalf("authorized: %d %+v", status, out)
	}
	for _, tc := range []struct{ method, params string }{{"test.denied", `{"namespace":"tenant-a"}`}, {"test.echo", `{"namespace":"tenant-b"}`}} {
		status, out = rpctest.Call(t, c, ts.URL, tc.method, tc.params)
		if status != 403 || out.Error == nil || out.Error.Code != -32003 {
			t.Fatalf("not forbidden: %d %+v", status, out)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unauthorized request executed")
	}
	for _, tc := range []struct {
		name           string
		identity       rpc.TLSConfig
		peer           string
		noCert, oldTLS bool
	}{{"unknown leaf same subject", unknown, server.CertFile, false, false}, {"wrong CA", outsider, server.CertFile, false, false}, {"expired leaf", expired, server.CertFile, false, false}, {"wrong server pin", client, unknown.CertFile, false, false}, {"no cert", client, server.CertFile, true, false}, {"TLS 1.2", client, server.CertFile, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			tc.identity.CAFile = server.CAFile
			cfg, e := rpc.ClientTLS(tc.identity, tc.peer)
			if e != nil {
				t.Fatal(e)
			}
			if tc.noCert {
				cfg.Certificates = nil
			}
			if tc.oldTLS {
				cfg.MinVersion = tls.VersionTLS12
				cfg.MaxVersion = tls.VersionTLS12
			}
			resp, e := rpctest.ClientConfig(t, cfg).Get(ts.URL + "/healthz")
			if e == nil {
				resp.Body.Close()
				t.Fatal("invalid identity accepted")
			}
		})
	}
	t.Run("wrong server hostname", func(t *testing.T) {
		cfg, e := rpc.ClientTLS(client, server.CertFile)
		if e != nil {
			t.Fatal(e)
		}
		cfg.ServerName = "wrong.example"
		resp, e := rpctest.ClientConfig(t, cfg).Get(ts.URL + "/healthz")
		if e == nil {
			resp.Body.Close()
			t.Fatal("wrong hostname accepted")
		}
	})

	for _, path := range []string{"/healthz", "/v1/models", "/v1/generate"} {
		r, e := c.Get(ts.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		want := 404
		if path == "/healthz" {
			want = 200
		}
		if r.StatusCode != want {
			t.Fatalf("%s: %d", path, r.StatusCode)
		}
	}
	// A forged header cannot override the actual leaf identity/permissions.
	req, _ := http.NewRequest("POST", ts.URL+"/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":"id","method":"test.echo","params":{"namespace":"tenant-b"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Cert", client.CertFile)
	req.Header.Set("X-Namespace", "tenant-a")
	resp, e := c.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("forwarded identity trusted")
	}
}
func TestStrictProfileNeverExecutesInvalidRequests(t *testing.T) {
	p := rpctest.NewPKI(t)
	identity := p.Issue("identity", false)
	var calls atomic.Int64
	s, e := rpc.NewServer(rpc.ServerConfig{Listen: "127.0.0.1:0", TLS: identity, Authorization: []rpc.Authorization{{ID: "client", CertFile: identity.CertFile, Methods: []string{"test.echo"}, Namespaces: []string{"*"}}}}, map[string]rpc.Method{"test.echo": func(ctx context.Context, raw json.RawMessage, stream *rpc.Stream) (any, *rpc.Error) {
		var args struct {
			Namespace string `json:"namespace"`
			N         int    `json:"n,omitempty"`
		}
		if rpc.Decode(raw, &args) != nil {
			return nil, rpc.InvalidParams()
		}
		calls.Add(1)
		return "ok", nil
	}}, 1024)
	if e != nil {
		t.Fatal(e)
	}
	ts := rpctest.Start(t, s)
	c := rpctest.Client(t, identity, identity.CertFile)
	cases := []struct {
		body string
		code int
	}{
		{`{`, -32700}, {`[]`, -32600}, {`null`, -32600}, {`{"jsonrpc":"2.0","method":"test.echo","params":{}}`, -32600},
		{`{"jsonrpc":"2.0","id":1,"method":"test.echo","params":{}}`, -32600},
		{`{"jsonrpc":"2.0","id":"","method":"test.echo","params":{}}`, -32600},
		{`{"jsonrpc":"2.0","id":"id","method":"test.echo","params":null}`, -32600},
		{`{"jsonrpc":"2.0","id":"id","method":"test.echo","params":[]}`, -32600},
		{`{"jsonrpc":"2.0","id":"id","method":"test.echo","params":{},"extra":0}`, -32600},
		{`{"jsonrpc":"2.0","id":"id","ID":"id","method":"test.echo","params":{}}`, -32600},
		{`{"jsonrpc":"2.0","id":"id","method":"test.echo","method":"other","params":{}}`, -32600},
		{`{"jsonrpc":"2.0","id":"id","method":"test.echo","params":{"namespace":"a","namespace":"b"}}`, -32600},
		{`{"jsonrpc":"2.0","id":"id","method":"test.echo","params":{"namespace":"a","x":0}}`, -32602},
		{`{"jsonrpc":"2.0","id":"id","method":"test.echo","params":{"namespace":"a","n":null}}`, -32602},
		{`{"jsonrpc":"2.0","id":"id","method":"test.echo","params":{"namespace":"a","n":1.2}}`, -32602},
		{`{"jsonrpc":"2.0","id":"id","method":"test.echo","params":{"namespace":"bad/namespace"}}`, -32602},
		{`{"jsonrpc":"2.0","id":"id","method":"test.echo","params":{"namespace":"é"}}`, -32602},
		{`{"jsonrpc":"1.0","id":"id","method":"test.echo","params":{"namespace":"a"}}`, -32600},
		{`{"jsonrpc":"2.0","id":"id","method":"unknown","params":{"namespace":"a"}}`, -32601},
		{strings.Repeat(" ", 1025), -32600},
	}
	for _, tc := range cases {
		_, out := rpctest.Raw(t, c, ts.URL, tc.body)
		if out.Error == nil || out.Error.Code != tc.code {
			t.Errorf("%s => %+v want %d", tc.body, out, tc.code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request executed")
	}
	r, e := c.Get(ts.URL + "/healthz")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("health authorization missing")
	}
}
func TestStrictDecodeOpaqueStateAndBounds(t *testing.T) {
	var v struct {
		State json.RawMessage `json:"state"`
	}
	good := `{"state":{"protocol":"vendor","value":{"signature":"opaque"}}}`
	if e := rpc.Decode([]byte(good), &v); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{`{"state":{"x":1,"x":2}}`, `{"State":{}}`, strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66), "{\"state\":\"\xff\"}"} {
		if rpc.Decode([]byte(bad), &v) == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
func TestTypedDecodesStrictlyBeforeCalling(t *testing.T) {
	type params struct {
		Namespace string          `json:"namespace"`
		N         int             `json:"n,omitempty"`
		State     json.RawMessage `json:"state,omitempty"`
	}
	type key struct{}
	var calls atomic.Int64
	var got params
	failure := rpc.Failure(-32000, "fixture_error")
	stream := &rpc.Stream{}
	method := rpc.Typed(func(ctx context.Context, p params, s *rpc.Stream) (any, *rpc.Error) {
		calls.Add(1)
		got = p
		if ctx.Value(key{}) != "ctx" || s != stream {
			t.Error("context or stream not passed through")
		}
		if p.N < 0 {
			return nil, failure
		}
		return "ok", nil
	})
	ctx := context.WithValue(context.Background(), key{}, "ctx")
	result, e := method(ctx, json.RawMessage(`{"namespace":"tenant-a","n":2,"state":{"x":[1]}}`), stream)
	if e != nil || result != "ok" || got.Namespace != "tenant-a" || got.N != 2 || string(got.State) != `{"x":[1]}` {
		t.Fatalf("valid params: %v %+v %+v", result, e, got)
	}
	if result, e = method(ctx, json.RawMessage(`{"namespace":"tenant-a","n":-1}`), stream); result != nil || e != failure {
		t.Fatalf("handler error not returned as-is: %v %+v", result, e)
	}
	for _, bad := range []string{`{`, `[]`, `null`, `"x"`, `{"namespace":"a","x":0}`, `{"Namespace":"a"}`, `{"namespace":"a","n":null}`, `{"namespace":"a","n":1.2}`, `{"namespace":"a","namespace":"b"}`, "{\"namespace\":\"\xff\"}"} {
		result, e = method(ctx, json.RawMessage(bad), stream)
		if result != nil || e == nil || e.Code != -32602 || e.Data.Code != "invalid_params" {
			t.Errorf("%q => %v %+v want invalid_params", bad, result, e)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2", calls.Load())
	}
}
