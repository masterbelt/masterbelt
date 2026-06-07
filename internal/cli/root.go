// Package cli implements the masterbelt command's subcommands: check, ir, and
// lsp.
package cli

import "github.com/spf13/cobra"

// RootCmd is the masterbelt root command every subcommand hangs off; main
// executes it after wiring the process context and logger.
var RootCmd = &cobra.Command{
	Use:           "masterbelt [subcommand]",
	Short:         "masterbelt is the toolchain for the masterbelt language",
	Long:          "masterbelt is the toolchain for the masterbelt language.\n\nRun a subcommand such as `masterbelt lsp` to start the language server.",
	SilenceUsage:  true,
	SilenceErrors: true,
}
