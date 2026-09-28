package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"easygo-agent/rpc"
	"easygo-agent/rpc/rpctest"
	"easygo-agent/services/ai-gateway/gateway"
)

func TestNativeRPCBoundaryAuthorizationAndRoute(t *testing.T) {
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "operator-model" || r.Header.Get("Authorization") != "Bearer dummy-provider-secret" || r.URL.Path != "/generate" {
			t.Error("configured provider identity changed")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, response)
	}))
	defer provider.Close()
	p := rpctest.NewPKI(t)
	identity := p.Issue("native-gateway", false)
	caller := p.Issue("workshop", false)
	limited := p.Issue("limited", false)
	stranger := p.Issue("stranger", false)
	t.Setenv("NATIVE_TEST_DUMMY_KEY", "dummy-provider-secret")
	s, err := New(Config{ServerConfig: rpc.ServerConfig{TLS: identity, Authorization: []rpc.Authorization{
		{ID: "workshop", CertFile: caller.CertFile, Methods: []string{"gateway.native"}, Namespaces: []string{"tenant"}},
		{ID: "limited", CertFile: limited.CertFile, Methods: []string{"gateway.models"}, Namespaces: []string{"tenant"}},
	}}, FileConfig: gateway.FileConfig{Models: map[string]gateway.FileModel{"selected": {Model: gateway.Model{Protocol: "responses", Endpoint: provider.URL + "/generate", Model: "operator-model"}, APIKeyEnv: "NATIVE_TEST_DUMMY_KEY"}}}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := rpctest.Start(t, s)
	client := rpctest.Client(t, caller, identity.CertFile)
	params := `{"namespace":"tenant","model":"selected","protocol":"responses","body":{"model":"child-model","input":"hi"}}`
	status, out := rpctest.Call(t, client, endpoint.URL, "gateway.native", params)
	if status != 200 || out.Error != nil {
		t.Fatalf("native RPC: %d %+v", status, out)
	}
	raw, _ := json.Marshal(out.Result)
	var result gateway.NativeResponse
	if err = json.Unmarshal(raw, &result); err != nil || string(result.Body) != response {
		t.Fatalf("result: %v %s", err, raw)
	}
	if strings.Contains(string(raw), "dummy-provider-secret") {
		t.Fatal("provider key reached native caller")
	}
	for _, tc := range []struct {
		name, params string
		client       *http.Client
		code         int
	}{
		{"namespace", strings.Replace(params, `"tenant"`, `"other"`, 1), client, -32003},
		{"method", params, rpctest.Client(t, limited, identity.CertFile), -32003},
		{"url", strings.TrimSuffix(params, "}") + `,"url":"http://127.0.0.1:1"}`, client, -32602},
		{"headers", strings.TrimSuffix(params, "}") + `,"headers":{"Authorization":"forged"}}`, client, -32602},
		{"protocol", strings.Replace(params, `"responses"`, `"anthropic"`, 1), client, -32602},
		{"duplicate", strings.TrimSuffix(params, "}") + `,"model":"another"}`, client, -32600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, out := rpctest.Call(t, tc.client, endpoint.URL, "gateway.native", tc.params)
			if out.Error == nil || out.Error.Code != tc.code {
				t.Fatalf("rejection: %+v", out)
			}
		})
	}
	// Trusted CA membership alone must not grant this method.
	unknown := rpctest.Client(t, stranger, identity.CertFile)
	resp, err := unknown.Post(endpoint.URL+"/rpc", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":"x","method":"gateway.native","params":`+params+`}`))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatalf("unlisted certificate accepted: %d", resp.StatusCode)
		}
	}
	wrongPin := rpctest.Client(t, caller, stranger.CertFile)
	if resp, err := wrongPin.Post(endpoint.URL+"/rpc", "application/json", strings.NewReader(`{}`)); err == nil {
		resp.Body.Close()
		t.Fatal("wrong server certificate pin accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("rejected request called provider: %d", calls.Load())
	}
}

func TestNativeRPCBoundarySanitizesProviderHTTPError(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, "dummy-private-provider-error")
	}))
	defer provider.Close()
	p := rpctest.NewPKI(t)
	identity := p.Issue("gateway", false)
	caller := p.Issue("workshop", false)
	s, err := New(Config{ServerConfig: rpc.ServerConfig{TLS: identity, Authorization: []rpc.Authorization{{ID: "workshop", CertFile: caller.CertFile, Methods: []string{"gateway.native"}, Namespaces: []string{"tenant"}}}}, FileConfig: gateway.FileConfig{Models: map[string]gateway.FileModel{"selected": {Model: gateway.Model{Protocol: "responses", Endpoint: provider.URL, Model: "operator"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := rpctest.Start(t, s)
	client := rpctest.Client(t, caller, identity.CertFile)
	resp, err := client.Post(endpoint.URL+"/rpc", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":"error","method":"gateway.native","params":{"namespace":"tenant","model":"selected","protocol":"responses","body":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out rpc.Envelope
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Error == nil || out.Error.Code != -32000 || strings.Contains(string(raw), "dummy-private-provider-error") {
		t.Fatalf("unsanitized error: %s", raw)
	}
}
