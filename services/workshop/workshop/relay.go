package workshop

import (
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
	"strings"
	"time"
)

// maxRelayBodyBytes bounds a relayed request (the gateway reply is bounded by
// rpc.Client's equal 16 MiB default); cmd/task-shim applies the same limit.
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
	tlsConfig, e := rpc.ClientTLS(cfg.TLS, cfg.PeerCertificateFile)
	if e != nil {
		return "", "", nil, e
	}
	// With a TLS config and valid limits, NewClient fails only on the endpoint
	// (https, host, no user info, query or fragment). Replies keep the client's
	// 16 MiB default, the same bound as maxRelayBodyBytes.
	client, e := rpc.NewClient(cfg.URL, tlsConfig, rpc.Options{Timeout: 120 * time.Second})
	if e != nil {
		return "", "", nil, errors.New("invalid model gateway URL")
	}
	listener, e := net.Listen(network, address)
	if e != nil {
		client.Close()
		return "", "", nil, e
	}
	secret := make([]byte, 32)
	if _, e = rand.Read(secret); e != nil {
		listener.Close()
		client.Close()
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
			http.Error(w, "model relay capacity exceeded", http.StatusTooManyRequests)
			return
		}
		credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Header.Get("X-Api-Key") != "" {
			credential = r.Header.Get("X-Api-Key")
		}
		if subtle.ConstantTimeCompare([]byte(credential), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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
		if r.Method != "POST" || r.URL.Path != path || (r.URL.RawQuery != "" && (profile.Protocol != "anthropic" || r.URL.RawQuery != "beta=true")) {
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
		// The child sees only a constant 502 text, never the gateway's error,
		// envelope or transport detail.
		var reply json.RawMessage
		if e = client.Call(callCtx, "gateway.native", map[string]any{"namespace": namespace, "model": profile.GatewayModel, "protocol": profile.Protocol, "body": object}, &reply); e != nil {
			var rejected *rpc.Error
			if errors.As(e, &rejected) {
				http.Error(w, "model gateway rejected request", http.StatusBadGateway)
			} else {
				http.Error(w, "model gateway unavailable", http.StatusBadGateway)
			}
			return
		}
		// Strict decode, as before rpc.Client: unknown fields and a null result fail.
		var result struct {
			ContentType string `json:"content_type"`
			Body        string `json:"body"`
		}
		if rpc.Decode(reply, &result) != nil {
			http.Error(w, "model gateway rejected request", http.StatusBadGateway)
			return
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(result.Body)
		if (result.ContentType != "application/json" && result.ContentType != "text/event-stream") || decodeErr != nil || len(decoded) > 8<<20 {
			http.Error(w, "invalid model response", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", result.ContentType)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(decoded) // headers are sent; a vanished child has nothing left to tell
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 15 * time.Second, WriteTimeout: 150 * time.Second}
	go func() {
		// stop's Close ends Serve with ErrServerClosed. Any other error leaves the
		// child without a relay, so its model calls and the run fail on their own;
		// stderr carries the controller's JSON log and must not get raw text.
		_ = server.Serve(listener)
	}()
	return "http://" + listener.Addr().String() + "/v1", token, func() { server.Close(); client.Close() }, nil
}
