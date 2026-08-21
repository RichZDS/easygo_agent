package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"easygo-agent/internal/app"
	"go.uber.org/zap"
)

// main exits with the terminal application's status code.
func main() {
	os.Exit(run())
}

// run parses command flags and runs the application with signal cancellation.
func run() int {
	configPath := flag.String("config", "configs/config.example.yaml", "path to the non-secret YAML configuration")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, *configPath); err != nil {
		if _, printErr := fmt.Fprintf(os.Stderr, "application stopped: %v\n", err); printErr != nil {
			zap.NewNop().Error("print application error failed", zap.Error(printErr))
		}
		return 1
	}
	return 0
}
