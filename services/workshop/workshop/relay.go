package workshop

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"easygo-agent/rpc"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxRelayBodyBytes bounds a relayed request and the gateway's response; the
// in-container proxy (cmd/task-shim) applies the same request limit.
const maxRelayBodyBytes = 16 << 20

// A task-local capability exposes only the selected model generation operation.
// The child never receives the gateway identity or vendor credential.
func startModelRelay(ctx context.Context, cfg *ModelGateway, namespace string, profile RuntimeProfile, crew ...CrewChannel) (string, string, func(), error) {
	return startModelRelayOn(ctx, cfg, namespace, profile, "tcp", "127.0.0.1:0", crew...)
}

func startModelRelayOn(ctx context.Context, cfg *ModelGateway, namespace string, profile RuntimeProfile, network, address string, crew ...CrewChannel) (string, string, func(), error) {
	if cfg == nil || !rpc.ValidNamespace(namespace) {
		return "", "", nil, errors.New("model gateway unavailable")
	}
	endpoint, e := url.Parse(cfg.URL)
	if e != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", "", nil, errors.New("invalid model gateway URL")
	}
	tlsConfig, e := rpc.ClientTLS(cfg.TLS, cfg.PeerCertificateFile)
	if e != nil {
		return "", "", nil, e
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport, Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	listener, e := net.Listen(network, address)
	if e != nil {
		return "", "", nil, e
	}
	secret := make([]byte, 32)
	if _, e = rand.Read(secret); e != nil {
		listener.Close()
		return "", "", nil, e
	}
	token := hex.EncodeToString(secret)
	path := map[string]string{"responses": "/v1/responses", "chat_completions": "/v1/chat/completions", "anthropic": "/v1/messages"}[profile.Protocol]
	slots := make(chan struct{}, 8)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "model relay capacity exceeded", 429)
			return
		}
		credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Header.Get("X-Api-Key") != "" {
			credential = r.Header.Get("X-Api-Key")
		}
		if subtle.ConstantTimeCompare([]byte(credential), []byte(token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/crew/") {
			if len(crew) == 0 || crew[0] == nil {
				http.NotFound(w, r)
				return
			}
			serveCrew(w, r, crew[0])
			return
		}
		if r.Method != "POST" || r.URL.Path != path || (r.URL.RawQuery != "" && !(profile.Protocol == "anthropic" && r.URL.RawQuery == "beta=true")) {
			http.Error(w, "unsupported model operation", 404)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRelayBodyBytes))
		var object map[string]json.RawMessage
		if e != nil || rpc.Decode(raw, &object) != nil || object == nil {
			http.Error(w, "invalid request", 400)
			return
		}
		callCtx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		idBytes := make([]byte, 16)
		if _, e = rand.Read(idBytes); e != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		id := hex.EncodeToString(idBytes)
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "gateway.native", "params": map[string]any{"namespace": namespace, "model": profile.GatewayModel, "protocol": profile.Protocol, "body": object}})
		request, e := http.NewRequestWithContext(callCtx, "POST", cfg.URL, bytes.NewReader(payload))
		if e != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		request.Header.Set("Content-Type", "application/json")
		response, e := client.Do(request)
		if e != nil {
			http.Error(w, "model gateway unavailable", 502)
			return
		}
		defer response.Body.Close()
		body, e := io.ReadAll(io.LimitReader(response.Body, maxRelayBodyBytes+1))
		var envelope struct {
			JSONRPC string `json:"jsonrpc"`
			ID      string `json:"id"`
			Result  *struct {
				ContentType string `json:"content_type"`
				Body        string `json:"body"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		if e != nil || len(body) > maxRelayBodyBytes || response.StatusCode != 200 || rpc.Decode(body, &envelope) != nil || envelope.JSONRPC != "2.0" || envelope.ID != id || envelope.Result == nil || len(envelope.Error) != 0 {
			http.Error(w, "model gateway rejected request", 502)
			return
		}
		result := envelope.Result
		decoded, decodeErr := base64.StdEncoding.DecodeString(result.Body)
		if (result.ContentType != "application/json" && result.ContentType != "text/event-stream") || decodeErr != nil || len(decoded) > 8<<20 {
			http.Error(w, "invalid model response", 502)
			return
		}
		w.Header().Set("Content-Type", result.ContentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Write(decoded)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 15 * time.Second, WriteTimeout: 150 * time.Second}
	go server.Serve(listener)
	return "http://" + listener.Addr().String() + "/v1", token, func() { server.Close(); transport.CloseIdleConnections() }, nil
}
