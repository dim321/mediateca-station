package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"mediateca-station/internal/agent"
	"mediateca-station/internal/config"
)

func main() {
	path := flag.String("config", "", "path to config.yaml")
	flag.Parse()
	if *path == "" {
		slog.Error("missing -config")
		os.Exit(1)
	}
	cfg, err := config.Load(*path)
	if err != nil {
		slog.Error("config", "err", err.Error())
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := agent.Run(ctx, cfg); err != nil {
		slog.Error("station", "err", err.Error())
		os.Exit(1)
	}
}
