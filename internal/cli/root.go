package cli

import "github.com/spf13/cobra"

var RootCmd = &cobra.Command{
	Use:           "masterbelt [subcommand]",
	Short:         "masterbelt is the toolchain for the masterbelt language",
	Long:          "masterbelt is the toolchain for the masterbelt language.\n\nRun a subcommand such as `masterbelt lsp` to start the language server.",
	SilenceUsage:  true,
	SilenceErrors: true,
}
