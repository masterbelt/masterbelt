package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/lsp"
)

func init() {
	RootCmd.AddCommand(LspCmd)
	// Many LSP clients launch a server as `<bin> lsp --stdio`. We always use
	// stdio, so accept the flag for compatibility and ignore it.
	LspCmd.Flags().Bool("stdio", true, "communicate over stdio (the default and only transport)")
	// --pprof starts a live net/http/pprof endpoint on the given localhost
	// address for interactive profiling of the resident server. It
	// is a plain local flag, NOT a persistent hook: a subcommand PersistentPreRun
	// would shadow the root's profiling hooks. It feeds the same
	// switch as the MASTERBELT_PPROF_ADDR env var; off by default.
	LspCmd.Flags().String("pprof", "", "serve net/http/pprof on this localhost address (e.g. localhost:6060) for live profiling")
}

// LspCmd is the lsp subcommand: it runs the masterbelt language server,
// speaking the Language Server Protocol over stdin and stdout.
var LspCmd = &cobra.Command{
	Use:   "lsp",
	Short: "Run the masterbelt language server over stdio",
	Long:  "Run the masterbelt language server, speaking the Language Server Protocol over stdin/stdout.\n\nEditors launch this and communicate on the process's standard input and output; logs go to stderr to keep the protocol channel clean.",
	Args:  cobra.NoArgs,

	RunE: func(cmd *cobra.Command, _ []string) error {
		// --pprof feeds the server's pprof switch through the env var the server
		// reads at construction; the flag wins when set, so `lsp --pprof=:6060`
		// works without the env var. The CLI runs one command per process, so
		// setting the process env here is safe and local.
		if addr, _ := cmd.Flags().GetString("pprof"); addr != "" {
			if err := os.Setenv("MASTERBELT_PPROF_ADDR", addr); err != nil {
				return err
			}
		}
		return lsp.ServeStdio(cmd.Context())
	},
}
