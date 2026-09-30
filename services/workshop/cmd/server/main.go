package main

import (
	"context"
	"easygo-agent/rpc"
	"easygo-agent/services/workshop/server"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	flags := flag.NewFlagSet("workshop", flag.ContinueOnError)
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
	httpServer, service, err := server.New(config)
	if err != nil {
		return err
	}
	defer service.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return rpc.Serve(ctx, httpServer)
}
