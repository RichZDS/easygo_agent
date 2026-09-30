// task-shim runs as PID 1 inside a network-none task container. Its only upstream
// is the per-invocation Unix socket, authenticated by the native CLI capability.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func main() { os.Exit(run(os.Args[1:])) }
func run(args []string) int {
	if len(args) == 2 && args[0] == "--verify-root" {
		raw, err := os.ReadFile("/workspace/marker")
		if err != nil || string(raw) != args[1] {
			return 1
		}
		fmt.Print(string(raw))
		return 0
	}
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "runtime required")
		return 1
	}
	switch args[0] {
	case "codex", "claude", "pi", "openclaw":
	default:
		return 1
	}
	socket := os.Getenv("EASYGO_RELAY_SOCKET")
	if socket != "/run/easygo-relay/model.sock" { // workshop runtimeRelaySocket
		return 1
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, MaxIdleConns: 8, MaxConnsPerHost: 8, ResponseHeaderTimeout: 125 * time.Second}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.Out.URL.Scheme = "http"
		r.Out.URL.Host = "relay"
		r.Out.Host = "relay"
	}, Transport: transport, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "model relay unavailable", http.StatusBadGateway)
	}}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20) // workshop maxRelayBodyBytes
		proxy.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 15 * time.Second}
	listener, err := net.Listen("tcp", "127.0.0.1:18080") // workshop runtimeProxyAddr
	if err != nil {
		return 1
	}
	defer server.Close()
	go func() {
		// Close (deferred above) ends Serve with ErrServerClosed; anything else
		// means the child lost its model/crew proxy mid-run.
		if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			_, _ = io.WriteString(os.Stderr, "model proxy stopped\n")
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cmd := exec.CommandContext(ctx, "/usr/local/bin/"+args[0], args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		_, _ = io.WriteString(os.Stderr, "native runtime failed to start\n")
		return 1
	}
	return 0
}
