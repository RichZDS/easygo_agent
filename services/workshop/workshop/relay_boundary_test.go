package workshop

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/rpc"
	"easygo-agent/rpc/rpctest"
)

func nativeRelayFixture(t *testing.T, method rpc.Method) *ModelGateway {
	t.Helper()
	p := rpctest.NewPKI(t)
	serverIdentity := p.Issue("gateway", false)
	clientIdentity := p.Issue("workshop", false)
	s, err := rpc.NewServer(rpc.ServerConfig{Listen: "127.0.0.1:0", TLS: serverIdentity, Authorization: []rpc.Authorization{{ID: "workshop", CertFile: clientIdentity.CertFile, Methods: []string{"gateway.native"}, Namespaces: []string{"task-namespace"}}}}, map[string]rpc.Method{"gateway.native": method}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	server := rpctest.Start(t, s)
	return &ModelGateway{URL: server.URL + "/rpc", TLS: clientIdentity, PeerCertificateFile: serverIdentity.CertFile}
}

func relayPost(t *testing.T, base, path, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", strings.TrimSuffix(base, "/v1")+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func TestNativeRelayBoundaryCapabilityAndForcedScope(t *testing.T) {
	for _, tc := range []struct{ protocol, path string }{{"responses", "/v1/responses"}, {"chat_completions", "/v1/chat/completions"}, {"anthropic", "/v1/messages"}} {
		t.Run(tc.protocol, func(t *testing.T) {
			var calls atomic.Int64
			cfg := nativeRelayFixture(t, func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
				calls.Add(1)
				var p struct {
					Namespace, Model, Protocol string
					Body                       map[string]any
				}
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Error(err)
				}
				if p.Namespace != "task-namespace" || p.Model != "operator-route" || p.Protocol != tc.protocol {
					t.Errorf("scope overwritten: %s", raw)
				}
				if p.Body["model"] != "child-spoof" {
					t.Error("fixture must exercise model override at next gateway boundary")
				}
				return map[string]any{"content_type": "application/json", "body": []byte(`{"native":"ok"}`)}, nil
			})
			base, token, closeRelay, err := startModelRelay(context.Background(), cfg, "task-namespace", RuntimeProfile{GatewayModel: "operator-route", Protocol: tc.protocol})
			if err != nil {
				t.Fatal(err)
			}
			defer closeRelay()
			if !strings.HasPrefix(base, "http://127.0.0.1:") || len(token) != 64 {
				t.Fatal("relay capability not ephemeral loopback")
			}
			body := `{"model":"child-spoof","namespace":"forged","url":"https://forbidden.invalid","headers":{"Authorization":"forged"}}`
			for _, negative := range []struct {
				path, credential, body string
				status                 int
			}{
				{tc.path, "wrong", body, 401}, {tc.path, "", body, 401}, {"/v1/models", token, body, 404}, {tc.path + "?url=forged", token, body, 404}, {tc.path, token, `null`, 400}, {tc.path, token, `{"model":1,"model":2}`, 400},
			} {
				status, _ := relayPost(t, base, negative.path, negative.credential, negative.body)
				if status != negative.status {
					t.Fatalf("rejection %s = %d want %d", negative.path, status, negative.status)
				}
			}
			if calls.Load() != 0 {
				t.Fatal("unauthorized local request reached gateway")
			}
			status, result := relayPost(t, base, tc.path, token, body)
			if status != 200 || result != `{"native":"ok"}` || calls.Load() != 1 {
				t.Fatalf("relay roundtrip %d %s count %d", status, result, calls.Load())
			}
			// Tokens are scoped per task, even if model and namespace match.
			base2, token2, close2, err := startModelRelay(context.Background(), cfg, "task-namespace", RuntimeProfile{GatewayModel: "operator-route", Protocol: tc.protocol})
			if err != nil {
				t.Fatal(err)
			}
			defer close2()
			if token2 == token {
				t.Fatal("capability reused across tasks")
			}
			status, _ = relayPost(t, base2, tc.path, token, body)
			if status != 401 || calls.Load() != 1 {
				t.Fatal("another task accepted token")
			}
		})
	}
}

func TestNativeRelayBoundaryCancelClosesUpstream(t *testing.T) {
	for _, mode := range []string{"task-context", "caller-context"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			closed := make(chan struct{})
			cfg := nativeRelayFixture(t, func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
				close(entered)
				<-ctx.Done()
				close(closed)
				return nil, rpc.Failure(-32000, "canceled")
			})
			ctx, cancelTask := context.WithCancel(context.Background())
			defer cancelTask()
			base, token, stop, err := startModelRelay(ctx, cfg, "task-namespace", RuntimeProfile{GatewayModel: "selected", Protocol: "responses"})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			requestCtx, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			req, _ := http.NewRequestWithContext(requestCtx, "POST", base+"/responses", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+token)
			done := make(chan struct{})
			go func() {
				defer close(done)
				client := &http.Client{Timeout: 20 * time.Second}
				resp, err := client.Do(req)
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("gateway not reached")
			}
			if mode == "task-context" {
				cancelTask()
			} else {
				cancelCaller()
			}
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream request not canceled")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("child HTTP request did not terminate")
			}
		})
	}
}

func TestNativeRelayBoundaryResponseLimitsAndErrors(t *testing.T) {
	for _, mode := range []string{"invalid-content-type", "decoded-overflow", "envelope-overflow", "upstream-error", "valid-sse"} {
		t.Run(mode, func(t *testing.T) {
			cfg := nativeRelayFixture(t, func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
				switch mode {
				case "upstream-error":
					return nil, &rpc.Error{Code: -32000, Message: "dummy-private-gateway-message"}
				case "invalid-content-type":
					return map[string]any{"content_type": "text/html", "body": []byte("dummy-private-response")}, nil
				case "decoded-overflow":
					return map[string]any{"content_type": "application/json", "body": []byte(strings.Repeat("x", (8<<20)+1))}, nil
				case "envelope-overflow":
					return map[string]any{"content_type": "application/json", "body": []byte(strings.Repeat("x", 13<<20))}, nil
				default:
					return map[string]any{"content_type": "text/event-stream", "body": []byte("data: [DONE]\n\n")}, nil
				}
			})
			base, token, stop, err := startModelRelay(context.Background(), cfg, "task-namespace", RuntimeProfile{GatewayModel: "selected", Protocol: "responses"})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			status, body := relayPost(t, base, "/v1/responses", token, `{}`)
			if mode == "valid-sse" {
				if status != 200 || body != "data: [DONE]\n\n" {
					t.Fatalf("SSE changed: %d %q", status, body)
				}
			} else if status != 502 || strings.Contains(body, "dummy-private") || len(body) > 128 {
				t.Fatalf("invalid upstream accepted/leaked: %d %d", status, len(body))
			}
		})
	}
}

func TestNativeRelayBoundaryRejectsWrongServerPin(t *testing.T) {
	var calls atomic.Int64
	cfg := nativeRelayFixture(t, func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
		calls.Add(1)
		return nil, nil
	})
	// A client certificate has the same test CA but is not the gateway pin.
	cfg.PeerCertificateFile = cfg.TLS.CertFile
	base, token, stop, err := startModelRelay(context.Background(), cfg, "task-namespace", RuntimeProfile{GatewayModel: "selected", Protocol: "responses"})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	status, _ := relayPost(t, base, "/v1/responses", token, `{}`)
	if status != 502 || calls.Load() != 0 {
		t.Fatal("wrong server pin reached RPC")
	}
}

func TestNativeRelayBoundaryProfileProtocolValidation(t *testing.T) {
	for _, p := range []RuntimeProfile{
		{Engine: "codex", Protocol: "chat_completions", BaseURL: "https://provider.invalid/v1", Model: "dummy", APIKeyEnv: "DUMMY_KEY"},
		{Engine: "claude", Protocol: "responses", BaseURL: "https://provider.invalid/v1", Model: "dummy", APIKeyEnv: "DUMMY_KEY"},
		{Engine: "codex", Protocol: "chat_completions", GatewayModel: "selected"},
		{Engine: "claude", Protocol: "responses", GatewayModel: "selected"},
		{Engine: "pi", Protocol: "custom", GatewayModel: "selected"},
		{Engine: "pi", Protocol: "responses", GatewayModel: "selected", APIKeyEnv: "DUMMY_KEY"},
		{Engine: "codex", Protocol: "responses", BaseURL: "https://user:dummy@example.invalid", Model: "dummy", APIKeyEnv: "DUMMY_KEY"},
		{Engine: "codex", Protocol: "responses", BaseURL: "https://example.invalid?key=dummy", Model: "dummy", APIKeyEnv: "DUMMY_KEY"},
	} {
		if err := p.validate(); err == nil {
			t.Errorf("unsupported/ambiguous profile accepted: %+v", p)
		}
	}
}
