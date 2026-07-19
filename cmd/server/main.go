package main

import (
	"flag"
	"fmt"
	"os"

	"easygo-agent/internal/app"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "path to YAML configuration file")
	flag.Parse()

	if err := app.Run(*configPath); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "application stopped: %v\n", err)
		os.Exit(1)
	}
}
