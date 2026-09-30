package main

import (
	"context"
	"easygo-agent/rpc"
	"easygo-agent/services/ai-gateway/gateway"
	"easygo-agent/services/ai-gateway/meter"
	"easygo-agent/services/ai-gateway/server"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	flags := flag.NewFlagSet("ai-gateway", flag.ContinueOnError)
	path := flags.String("config", "", "JSON configuration file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || flags.NArg() != 0 {
		return errors.New("--config FILE is required")
	}
	file, err := os.Open(*path)
	if err != nil {
		return fmt.Errorf("cannot open configuration file: %w", err)
	}
	config, err := server.LoadConfig(file)
	file.Close()
	if err != nil {
		return err
	}
	log := rpc.JSONLogger(os.Stderr)
	config.Audit = func(a rpc.Audit) { log(a) }
	config.Observer = func(o gateway.Observation) {
		log(struct {
			Kind string `json:"kind"`
			gateway.Observation
		}{"model", o})
	}
	if config.Meter != nil {
		config.Meter.Observer = meterLog(log)
		manager, e := meter.New(*config.Meter, config.TLS)
		if e != nil {
			return e
		}
		defer manager.Close()
		config.Billing = manager
	}
	httpServer, err := server.New(config)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return rpc.Serve(ctx, httpServer)
}

// meterLog records meter flush reports, which arrive every second, only when
// the backlog or the error changes or claims expired. Reports carry counts and
// error text, never request IDs.
func meterLog(log func(any)) func(meter.FlushReport) {
	var last meter.FlushReport
	return func(r meter.FlushReport) {
		if r.Expired == 0 && r.Pending == last.Pending && r.Error == last.Error {
			return
		}
		last = r
		log(struct {
			Kind string    `json:"kind"`
			Time time.Time `json:"time"`
			meter.FlushReport
		}{"meter", time.Now().UTC(), r})
	}
}
