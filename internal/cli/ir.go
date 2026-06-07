package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(IRCmd)
	IRCmd.Flags().String("format", "text", "output format: text or json")
}

// IRCmd dumps a file's resolved IR in the exact text representation (F-4):
// the typed value graph, the folded constants, the resolved references — the
// same form the .ir snapshots pin and (*ir.Module).UnmarshalText reads back.
// The json format wraps the text in a JSON envelope; the tree marshals
// through encoding.TextMarshaler, which encoding/json picks up on its own —
// the stdlib dividend of the text contract, demonstrated in production here.
var IRCmd = &cobra.Command{
	Use:   "ir <file>",
	Short: "Print a file's resolved IR in the exact text representation",
	Long: "Analyze one masterbelt source file and print its resolved IR — the typed value graph, " +
		"the folded constants, the resolved references — in the exact text representation. " +
		"The output is machine-parseable: (*ir.Module).UnmarshalText reads it back.",
	Args: cobra.ExactArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		module, _ := semantic.Analyze(abstract.NewDocument(data))

		format, _ := cmd.Flags().GetString("format")
		switch format {
		case "text":
			text, err := module.MarshalText()
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(text)
			return err
		case "json":
			// The module implements encoding.TextMarshaler, so json.Marshal
			// embeds the exact text form with no further code.
			doc, err := json.MarshalIndent(map[string]any{
				"file": filepath.ToSlash(args[0]),
				"ir":   module,
			}, "", "  ")
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(doc))
			return err
		default:
			return fmt.Errorf("unknown format %q (want text or json)", format)
		}
	},
}
