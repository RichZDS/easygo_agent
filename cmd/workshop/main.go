package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"easygo-agent/pkg/workshop"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "configs/workshop.example.json", "operator workflow configuration")
	listen := flag.String("listen", "127.0.0.1:8091", "HTTP listen address (loopback by default)")
	flag.Parse()
	file, err := os.Open(*configPath)
	if err != nil {
		return err
	}
	var cfg workshop.Config
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&cfg)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("config must contain one JSON object")
		}
	}
	closeErr := file.Close()
	if err != nil {
		return errors.New("invalid workshop configuration")
	}
	if closeErr != nil {
		return closeErr
	}
	token := ""
	if cfg.BearerTokenEnv != "" {
		token = os.Getenv(cfg.BearerTokenEnv)
		if token == "" {
			return fmt.Errorf("bearer_token_env %s is empty", cfg.BearerTokenEnv)
		}
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); (ip == nil || !ip.IsLoopback()) && token == "" {
		return errors.New("non-loopback listener requires bearer_token_env")
	}
	service, err := workshop.New(cfg, nil)
	if err != nil {
		return err
	}
	defer service.Close()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: workshop.Handler(service, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Printf("workshop listening on %s", listener.Addr())
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
	}
	return service.Close()
}
