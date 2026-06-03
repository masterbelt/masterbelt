package lsp

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/owenrumney/go-lsp/server"
)

// ServeStdio runs the masterbelt language server, speaking LSP over the
// process's standard input and output, until the client disconnects.
//
// It owns all of the protocol-library wiring — building the JSON-RPC server,
// choosing the stdio transport, and deciding what counts as a clean exit — so
// that callers depend only on this package and never on the underlying LSP
// library. A closed stdin (io.EOF) is how an editor signals a normal shutdown,
// so it is reported as success; any other failure is returned.
//
// Logs are emitted through slog.Default(); point it at stderr (not stdout) so
// the protocol channel stays uncorrupted.
func ServeStdio(ctx context.Context) error {
	srv := server.NewServer(NewServer(), server.WithLogger(slog.Default()))
	if err := srv.Run(ctx, server.RunStdio()); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
