package rpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"easygo-agent/rpc"
	"easygo-agent/rpc/rpctest"
)

func TestNilSuccessAndErrorEnvelopes(t *testing.T) {
	p := rpctest.NewPKI(t)
	identity := p.Issue("identity", false)
	methods := map[string]rpc.Method{
		"test.nil": func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil },
		"test.stream": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			if e := s.Result(nil); e != nil {
				t.Error(e)
			}
			return nil, nil
		},
		"test.error": func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) {
			return "must not leak", rpc.Failure(-32000, "fixture_error")
		},
	}
	server, e := rpc.NewServer(rpc.ServerConfig{Listen: "127.0.0.1:0", TLS: identity, Authorization: []rpc.Authorization{{ID: "verified", CertFile: identity.CertFile, Methods: []string{"*"}, Namespaces: []string{"tenant"}}}}, methods, 0)
	if e != nil {
		t.Fatal(e)
	}
	ts := rpctest.Start(t, server)
	c := rpctest.Client(t, identity, identity.CertFile)
	for _, method := range []string{"test.nil", "test.stream", "test.error"} {
		t.Run(method, func(t *testing.T) {
			resp, e := c.Post(ts.URL+"/rpc", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":"nil-id","method":"`+method+`","params":{"namespace":"tenant"}}`))
			if e != nil {
				t.Fatal(e)
			}
			raw, e := io.ReadAll(resp.Body)
			resp.Body.Close()
			if e != nil {
				t.Fatal(e)
			}
			if method == "test.stream" {
				if resp.Header.Get("Content-Type") != "text/event-stream" || !bytes.HasPrefix(raw, []byte("event: result\ndata: ")) {
					t.Fatalf("invalid SSE: %s", raw)
				}
				raw = bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("event: result\ndata: ")))
			}
			var wire map[string]json.RawMessage
			if e = json.Unmarshal(raw, &wire); e != nil {
				t.Fatal(e)
			}
			if method == "test.error" {
				if _, ok := wire["result"]; ok {
					t.Fatalf("result in error: %s", raw)
				}
				if _, ok := wire["error"]; !ok {
					t.Fatal("missing error")
				}
			} else {
				if string(wire["result"]) != "null" {
					t.Fatalf("missing explicit null result: %s", raw)
				}
				if _, ok := wire["error"]; ok {
					t.Fatal("error in success")
				}
			}
			var decoded rpc.Envelope
			if e = json.Unmarshal(raw, &decoded); e != nil || decoded.ID != "nil-id" {
				t.Fatalf("Envelope decoding failed: %v %+v", e, decoded)
			}
		})
	}
}

func TestAuditVerifiedIdentityAndFailureMetadata(t *testing.T) {
	p := rpctest.NewPKI(t)
	identity := p.Issue("identity", false)
	var mu sync.Mutex
	var audits []rpc.Audit
	observer := func(a rpc.Audit) { mu.Lock(); defer mu.Unlock(); audits = append(audits, a) }
	server, e := rpc.NewServer(rpc.ServerConfig{Listen: "127.0.0.1:0", TLS: identity, Audit: observer, Authorization: []rpc.Authorization{{ID: "verified-principal", CertFile: identity.CertFile, Methods: []string{"test.ok", "test.fail", "test.stream"}, Namespaces: []string{"tenant"}}}}, map[string]rpc.Method{
		"test.ok": func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil },
		"test.fail": func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) {
			return nil, rpc.Failure(-32009, "conflict")
		},
		"test.stream": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			if e := s.Delta("private delta"); e != nil {
				t.Error(e)
			}
			return nil, rpc.Failure(-32000, "upstream_error")
		},
	}, 0)
	if e != nil {
		t.Fatal(e)
	}
	ts := rpctest.Start(t, server)
	c := rpctest.Client(t, identity, identity.CertFile)
	for _, tc := range []struct{ method, ns, id string }{{"test.ok", "tenant", "ok"}, {"test.fail", "tenant", "fail"}, {"test.ok", "forbidden", "denied"}, {"test.stream", "tenant", "stream"}} {
		req, _ := http.NewRequest("POST", ts.URL+"/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":"`+tc.id+`","method":"`+tc.method+`","params":{"namespace":"`+tc.ns+`","secret":"private prompt"}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Principal-ID", "forged")
		req.Header.Set("Authorization", "Bearer private-key")
		resp, e := c.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_, e = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	ts.CloseClientConnections()
	mu.Lock()
	defer mu.Unlock()
	if len(audits) != 4 {
		t.Fatalf("audit records %d", len(audits))
	}
	for _, a := range audits {
		if a.PrincipalID != "verified-principal" || a.Duration <= 0 || a.Kind != "rpc" {
			t.Fatalf("invalid audit %+v", a)
		}
		switch a.RequestID {
		case "ok":
			if a.Status != 200 || a.ErrorCode != "" {
				t.Fatalf("success %+v", a)
			}
		case "fail":
			if a.Status != 200 || a.ErrorCode != "conflict" {
				t.Fatalf("failure %+v", a)
			}
		case "denied":
			if a.Status != 403 || a.ErrorCode != "forbidden" {
				t.Fatalf("denial %+v", a)
			}
		case "stream":
			if a.Status != 200 || a.ErrorCode != "upstream_error" {
				t.Fatalf("stream failure %+v", a)
			}
		default:
			t.Fatalf("unexpected id %+v", a)
		}
	}
	raw, _ := json.Marshal(audits)
	for _, secret := range []string{"private prompt", "private-key", "private delta", "forged"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("audit contains non-metadata")
		}
	}
}

func TestJSONLoggerConcurrent(t *testing.T) {
	var output bytes.Buffer
	log := rpc.JSONLogger(&output)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log(rpc.Audit{Kind: "rpc", RequestID: "id", PrincipalID: "verified", Status: 200})
		}()
	}
	wg.Wait()
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) != 32 {
		t.Fatalf("record count %d", len(lines))
	}
	for _, line := range lines {
		var a rpc.Audit
		if json.Unmarshal(line, &a) != nil || a.PrincipalID != "verified" {
			t.Fatalf("interleaved JSON: %s", line)
		}
	}
}
