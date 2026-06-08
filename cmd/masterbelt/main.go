// Command masterbelt is the language's CLI: check (parse and type-check a
// project or file), ir (the exact IR dump), and lsp (the language server).
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

	// The default logger is set up by the root command's pre-run, so its handler
	// can honour --format; until then slog's built-in default carries any early
	// error.
	if err := cli.RootCmd.ExecuteContext(ctx); err != nil {
		slog.Error("masterbelt exited", "err", err)
		return 1
	}
	return 0
}
