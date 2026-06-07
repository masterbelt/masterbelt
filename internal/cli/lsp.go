package cli

import (
	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/lsp"
)

func init() {
	RootCmd.AddCommand(LspCmd)
	// Many LSP clients launch a server as `<bin> lsp --stdio`. We always use
	// stdio, so accept the flag for compatibility and ignore it.
	LspCmd.Flags().Bool("stdio", true, "communicate over stdio (the default and only transport)")
}

// LspCmd is the lsp subcommand: it runs the masterbelt language server,
// speaking the Language Server Protocol over stdin and stdout.
var LspCmd = &cobra.Command{
	Use:   "lsp",
	Short: "Run the masterbelt language server over stdio",
	Long:  "Run the masterbelt language server, speaking the Language Server Protocol over stdin/stdout.\n\nEditors launch this and communicate on the process's standard input and output; logs go to stderr to keep the protocol channel clean.",
	Args:  cobra.NoArgs,

	RunE: func(cmd *cobra.Command, _ []string) error {
		return lsp.ServeStdio(cmd.Context())
	},
}
