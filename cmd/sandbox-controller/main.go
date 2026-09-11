// Command sandbox-controller runs the trusted local Docker sandbox control
// plane. It is the only EasyGo process allowed to access the Docker socket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"easygo-agent/internal/sandbox"
)

const defaultConfigPath = "configs/sandbox-controller.yaml"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "sandbox-controller:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("sandbox-controller", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultConfigPath, "strict controller YAML path")
	healthcheckURL := flags.String("healthcheck", "", "check one controller health URL and exit")
	cleanup := flags.Bool("cleanup", false, "destroy all managed containers, volumes, and state, then exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(*healthcheckURL) != "" {
		return checkHealth(ctx, strings.TrimSpace(*healthcheckURL), stdout)
	}

	cfg, err := sandbox.LoadConfig(*configPath, lookupEnv)
	if err != nil {
		return err
	}
	engine, err := sandbox.NewDockerEngine(cfg.DockerHost)
	if err != nil {
		return err
	}
	defer engine.Close()
	manager, err := sandbox.BootstrapManager(ctx, cfg, engine, nil)
	if err != nil {
		return err
	}
	defer manager.Close()
	if *cleanup {
		if err := manager.Cleanup(ctx); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, "sandbox cleanup complete")
		return nil
	}
	if err := manager.Reconcile(ctx); err != nil {
		return err
	}
	handler, err := sandbox.NewHTTPHandler(cfg.AuthToken, manager)
	if err != nil {
		return err
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext: func(net.Listener) context.Context {
			return runContext
		},
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()
	reaperErrors := make(chan error, 1)
	go func() {
		reaperErrors <- manager.RunReaper(runContext)
	}()

	var runErr error
	reaperStopped := false
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("serve controller HTTP: %w", err)
		}
	case err := <-reaperErrors:
		reaperStopped = true
		if err != nil {
			runErr = fmt.Errorf("sandbox reaper stopped: %w", err)
		}
	}
	cancel()
	httpShutdownContext, httpShutdownCancel := context.WithTimeout(context.Background(), 8*time.Second)
	if err := server.Shutdown(httpShutdownContext); err != nil && !errors.Is(err, context.Canceled) {
		runErr = errors.Join(runErr, fmt.Errorf("shutdown controller HTTP: %w", err))
		if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			runErr = errors.Join(runErr, fmt.Errorf("force-close controller HTTP: %w", closeErr))
		}
	}
	httpShutdownCancel()

	// Docker cleanup receives its own deadline. A slow HTTP drain must never
	// consume the time reserved for stopping sandbox compute.
	hibernateContext, hibernateCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := manager.HibernateAll(hibernateContext); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("hibernate sandboxes on shutdown: %w", err))
	}
	hibernateCancel()
	if !reaperStopped {
		reaperWaitContext, reaperWaitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer reaperWaitCancel()
		select {
		case err := <-reaperErrors:
			if err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("sandbox reaper stopped: %w", err))
			}
		case <-reaperWaitContext.Done():
			runErr = errors.Join(runErr, errors.New("sandbox reaper did not stop before shutdown deadline"))
		}
	}
	return runErr
}

func checkHealth(ctx context.Context, endpoint string, output io.Writer) error {
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return errors.New("healthcheck must be an absolute http or https URL without credentials")
	}
	healthContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(healthContext, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return fmt.Errorf("create healthcheck request: %w", err)
	}
	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("controller healthcheck failed: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("controller healthcheck returned %s", response.Status)
	}
	_, _ = fmt.Fprintln(output, "ok")
	return nil
}
