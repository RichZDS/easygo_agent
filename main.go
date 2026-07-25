package main

import (
	"fmt"
	"os"

	"easygo-agent/internal/app"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load(".env")
	if err := app.Run("configs/config.yaml"); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "application stopped: %v\n", err)
		os.Exit(1)
	}
}
