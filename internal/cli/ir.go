package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic/reporter"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/source"
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
//
// A file that does not check cleanly gets its diagnostics, not a poisoned
// graph: "the resolved IR" is the command's contract, and a partial module
// printed silently would betray it. The analysis is the same one-file program
// `masterbelt check` runs, so the two commands cannot disagree about a file.
var IRCmd = &cobra.Command{
	Use:   "ir <file>",
	Short: "Print a file's resolved IR in the exact text representation",
	Long: "Analyze one masterbelt source file and print its resolved IR — the typed value graph, " +
		"the folded constants, the resolved references — in the exact text representation. " +
		"The output is machine-parseable: (*ir.Module).UnmarshalText reads it back. " +
		"A file with errors gets its diagnostics instead of a partial graph.",
	Args: cobra.ExactArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}

		// The same one-file pipeline checkSource runs: lexer, parser, and
		// semantic diagnostics aggregated, the module read off the program.
		doc := abstract.NewDocument(data)
		prog := semantic.NewProgram()
		id := semantic.FileID(displayPath(args[0]))
		prog.SetFile(id, doc, nil)
		prog.Refresh()

		var raw []diagnostic.Diagnostic
		raw = append(raw, doc.Concrete().LexDiagnostics()...)
		raw = append(raw, doc.Diagnostics()...)
		raw = append(raw, prog.Diagnostics(id)...)
		if len(raw) > 0 {
			rep := reporter.NewText(cmd.ErrOrStderr(), diagnostic.DefaultLocale)
			rep.Report(source.NewFile(displayPath(args[0]), data), raw)
			if err := rep.Flush(); err != nil {
				return err
			}
			if rep.Errors() > 0 {
				return fmt.Errorf("%s: %d error(s)", displayPath(args[0]), rep.Errors())
			}
		}
		module := prog.Module(id)

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
			out, err := json.MarshalIndent(map[string]any{
				"file": displayPath(args[0]),
				"ir":   module,
			}, "", "  ")
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return err
		default:
			return fmt.Errorf("unknown format %q (want text or json)", format)
		}
	},
}
