package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/masterbelt/masterbelt/internal/cli"
)

func main() {
	// Setup context with signal
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(logger)

	if err := cli.RootCmd.ExecuteContext(ctx); err != nil {
		logger.Error("masterbelt exited", "err", err)
		os.Exit(1)
	}
}
