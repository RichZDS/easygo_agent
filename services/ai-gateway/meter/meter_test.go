package meter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"easygo-agent/rpc"
	"easygo-agent/rpc/rpctest"
	"easygo-agent/services/ai-gateway/ai"
	"easygo-agent/services/ai-gateway/gateway"
)

type walletFixture struct {
	mu           sync.Mutex
	reservations map[string]gateway.Reservation
	settled      map[string]receipt
	charges      int
	loseAck      bool
	deny         bool
	config       Config
	identity     rpc.TLSConfig
}

func newWallet(t *testing.T) *walletFixture {
	t.Helper()
	pki := rpctest.NewPKI(t)
	serverID := pki.Issue("agent-loop", false)
	callerID := pki.Issue("gateway", false)
	f := &walletFixture{reservations: map[string]gateway.Reservation{}, settled: map[string]receipt{}, identity: callerID}
	server, e := rpc.NewServer(rpc.ServerConfig{Listen: "127.0.0.1:0", TLS: serverID, Authorization: []rpc.Authorization{{ID: "gateway", CertFile: callerID.CertFile, Methods: []string{"platform.wallet.reserve", "platform.wallet.settle"}, Namespaces: []string{"user-one"}}}}, map[string]rpc.Method{
		"platform.wallet.reserve": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			var p gateway.Reservation
			if rpc.Decode(raw, &p) != nil {
				return nil, rpc.InvalidParams()
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.deny {
				return nil, rpc.Failure(-32002, "insufficient_credits")
			}
			_, duplicate := f.reservations[p.RequestID]
			f.reservations[p.RequestID] = p
			return map[string]any{"reservation_id": p.RequestID, "tariff_version": 1, "reserved_micros": 100000, "duplicate": duplicate}, nil
		},
		"platform.wallet.settle": func(ctx context.Context, raw json.RawMessage, s *rpc.Stream) (any, *rpc.Error) {
			var p receipt
			if rpc.Decode(raw, &p) != nil {
				return nil, rpc.InvalidParams()
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, ok := f.settled[p.RequestID]; !ok {
				f.settled[p.RequestID] = p
				if p.Usage.Known {
					f.charges++
				}
			}
			if f.loseAck {
				return nil, rpc.Failure(-32603, "lost_ack")
			}
			return map[string]any{"status": "settled"}, nil
		},
	}, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	started := rpctest.Start(t, server)
	f.config = Config{URL: started.URL + "/rpc", PeerCertificateFile: serverID.CertFile, Database: filepath.Join(t.TempDir(), "meter.db")}
	return f
}
func newMeter(t *testing.T, f *walletFixture) *Manager {
	t.Helper()
	m, e := New(f.config, f.identity)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func modelGateway(t *testing.T, m *Manager, wallet *walletFixture, count *int) *gateway.Gateway {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wallet.mu.Lock()
		defer wallet.mu.Unlock()
		if len(wallet.reservations) == 0 {
			t.Error("provider called before credit reservation")
		}
		*count++
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		if payload["max_completion_tokens"] != float64(32) {
			t.Errorf("output cap not applied: %v", payload["max_completion_tokens"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4}}}`))
	}))
	t.Cleanup(upstream.Close)
	g, e := gateway.New(gateway.Config{Models: map[string]gateway.Model{"chat": {Protocol: "chat_completions", Endpoint: upstream.URL, Model: "fixture"}}, Billing: m, BillingMaxOutputTokens: 32})
	if e != nil {
		t.Fatal(e)
	}
	return g
}
func TestCreditAdmissionAndDurableReceiptReplay(t *testing.T) {
	wallet := newWallet(t)
	wallet.loseAck = true
	m := newMeter(t, wallet)
	count := 0
	g := modelGateway(t, m, wallet, &count)
	request := ai.Request{RequestID: "paid-once", Model: "chat", Messages: []ai.Message{{Role: "user", Content: []ai.Block{{Type: "text", Text: "hello"}}}}}
	ctx := gateway.WithNamespace(context.Background(), "user-one")
	response, e := g.Complete(ctx, request, nil)
	if e != nil || response.Message.Content[0].Text != "ok" {
		t.Fatal(e)
	}
	if _, e = g.Complete(ctx, request, nil); e == nil {
		t.Fatal("duplicate provider execution allowed")
	}
	wallet.mu.Lock()
	wallet.deny = true
	wallet.mu.Unlock()
	request.RequestID = "denied"
	_, e = g.Complete(ctx, request, nil)
	var ge *gateway.Error
	if !errors.As(e, &ge) || ge.Code != "insufficient_credits" {
		t.Fatalf("credit error lost: %v", e)
	}
	if count != 1 {
		t.Fatalf("provider called %d times", count)
	}
	if e = m.Flush(context.Background()); e == nil {
		t.Fatal("lost settlement ack not modeled")
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	wallet.mu.Lock()
	wallet.loseAck = false
	wallet.mu.Unlock()
	m = newMeter(t, wallet)
	defer m.Close()
	if e = m.Flush(context.Background()); e != nil {
		t.Fatal(e)
	}
	wallet.mu.Lock()
	defer wallet.mu.Unlock()
	p := wallet.settled["paid-once"]
	if wallet.charges != 1 || p.Usage.InputTokens != 10 || p.Usage.OutputTokens != 3 || p.Usage.CacheReadTokens != 4 || p.Namespace != "user-one" || p.Outcome != "complete" {
		t.Fatalf("usage replay wrong: %+v charges=%d", p, wallet.charges)
	}
	if wallet.reservations["paid-once"].InputTokens <= 1024 || wallet.reservations["paid-once"].OutputTokens != 32 {
		t.Fatal("bounds not reserved")
	}
}
func TestCrashAfterAdmissionRemainsUncertain(t *testing.T) {
	wallet := newWallet(t)
	m := newMeter(t, wallet)
	_, e := m.Reserve(context.Background(), gateway.Reservation{Namespace: "user-one", RequestID: "interrupted", Fingerprint: "abc", Model: "chat", Source: "gateway.native", InputTokens: 100, OutputTokens: 32})
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	m = newMeter(t, wallet)
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = m.Flush(ctx); e != nil {
		t.Fatal(e)
	}
	wallet.mu.Lock()
	defer wallet.mu.Unlock()
	r := wallet.settled["interrupted"]
	if r.Outcome != "uncertain" || r.Usage.Known || wallet.charges != 0 {
		t.Fatalf("crash treated as billable/free: %+v", r)
	}
}
