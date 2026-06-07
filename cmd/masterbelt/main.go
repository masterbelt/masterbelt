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
	os.Exit(run())
}

// run executes the CLI and returns the process exit code. os.Exit lives in
// main alone so run's deferred signal-handler release always executes.
func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(logger)

	if err := cli.RootCmd.ExecuteContext(ctx); err != nil {
		logger.Error("masterbelt exited", "err", err)
		return 1
	}
	return 0
}
