// ai-gateway serves the provider-neutral HTTP model protocol.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"easygo-agent/pkg/gateway"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	flags := flag.NewFlagSet("ai-gateway", flag.ContinueOnError)
	path := flags.String("config", "", "JSON configuration file (credentials via environment references)")
	listen := flags.String("listen", "127.0.0.1:8090", "HTTP listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("-config is required")
	}
	f, err := os.Open(*path)
	if err != nil {
		return errors.New("cannot open configuration file")
	}
	config, handler, err := gateway.LoadConfig(f)
	f.Close()
	if err != nil {
		return err
	}
	if err := validateListen(*listen, handler.BearerToken); err != nil {
		return err
	}
	config.Observer = jsonObserver(os.Stderr)
	g, err := gateway.New(config)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: gateway.NewHandler(g, handler), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("gateway HTTP server failed")
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		return errors.New("gateway shutdown timed out")
	}
	return nil
}

func validateListen(address, token string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("invalid listen address")
	}
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if !loopback && token == "" {
		return errors.New("non-loopback listen requires bearer_token_env")
	}
	return nil
}
func jsonObserver(w io.Writer) gateway.Observer {
	var mu sync.Mutex
	encoder := json.NewEncoder(w)
	return func(o gateway.Observation) { mu.Lock(); defer mu.Unlock(); _ = encoder.Encode(o) }
}
