package main

import (
	"flag"
	"log/slog"
	"os"

	"interlink/internal/gateway"
)

func main() {
	cfg := flag.String("config", "config/config.yaml", "path to config file")
	flag.Parse()

	// Fix 17: initialize structured logging with JSON handler.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := gateway.Run(*cfg); err != nil {
		slog.Error("gateway exited with error", "error", err)
		os.Exit(1)
	}
}
