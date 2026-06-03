package cli

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lsp"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(LspCmd)
}

var LspCmd = &cobra.Command{
	Use:   "lsp",
	Short: "Run the masterbelt language server over stdio",
	Long:  "Run the masterbelt language server, speaking the Language Server Protocol over stdin/stdout.\n\nEditors launch this and communicate on the process's standard input and output; logs go to stderr to keep the protocol channel clean.",
	Args:  cobra.NoArgs,

	RunE: func(cmd *cobra.Command, args []string) error {
		return lsp.ServeStdio(cmd.Context())
	},
}
