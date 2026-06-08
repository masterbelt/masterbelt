package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/pkg/belt/lexer"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/concrete"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
)

func init() {
	RootCmd.AddCommand(DumpCmd)
}

// DumpCmd prints a file's representation at one compilation stage — its tokens,
// concrete syntax tree, abstract syntax tree, or resolved IR — each in the exact
// text form that stage round-trips through (the same forms the snapshots pin).
//
// The earlier stages render whatever the source lexes and parses to, errors and
// all: a token stream, a lossless CST, and an AST exist for any input. ir alone
// withholds a poisoned graph — it reports the diagnostics instead, since "the
// resolved IR" is only meaningful when the file checks, the same one-file
// analysis `masterbelt check` runs.
var DumpCmd = &cobra.Command{
	Use:   "dump [token|cst|ast|ir] <file>",
	Short: "Print a file's representation at a compilation stage",
	Long: "Print one masterbelt source file's representation at a compilation stage:\n\n" +
		"  token  the lexer's token stream\n" +
		"  cst    the lossless concrete syntax tree\n" +
		"  ast    the abstract syntax tree\n" +
		"  ir     the resolved, typed IR\n\n" +
		"token, cst, and ast render whatever the source lexes and parses to, errors and all. " +
		"ir reports the diagnostics instead of a partial graph when the file does not check.",
	Args: cobra.ExactArgs(2),

	RunE: func(cmd *cobra.Command, args []string) error {
		stage, path := args[0], args[1]
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		d, err := dumpAt(stage, path, data)
		if err != nil {
			return err
		}

		// The stage's diagnostics go to stderr, the dump to stdout. ir gates on
		// an error — its text is meaningless on a file that did not check — while
		// the earlier stages dump regardless: their representation is faithful.
		if len(d.diags) > 0 {
			rep, err := newReporter(cmd, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			rep.Report(source.NewFile(displayPath(path), data), d.diags)
			if err := rep.Flush(); err != nil {
				return err
			}
			if d.gate && rep.Errors() > 0 {
				return fmt.Errorf("%s: %d error(s)", displayPath(path), rep.Errors())
			}
		}
		return writeDump(cmd, stage, displayPath(path), d.text)
	},
}

// stageDump is one stage's rendered text and the diagnostics gathered producing
// it. gate marks the ir stage, whose text is withheld on an error.
type stageDump struct {
	text  []byte
	diags []diagnostic.Diagnostic
	gate  bool
}

// dumpAt renders the named stage of the source, or an error for an unknown one.
func dumpAt(stage, path string, data []byte) (stageDump, error) {
	switch stage {
	case "token":
		return dumpTokens(path, data), nil
	case "cst":
		return dumpCST(data)
	case "ast":
		return dumpAST(data)
	case "ir":
		return dumpIR(path, data)
	default:
		return stageDump{}, fmt.Errorf("unknown stage %q (want token, cst, ast, or ir)", stage)
	}
}

// dumpTokens renders the lexer's token stream, one "Kind@offset+width" per line
// with the lexeme it covers.
func dumpTokens(path string, data []byte) stageDump {
	lex := lexer.New(source.NewFile(displayPath(path), data))
	toks := lex.Tokens()
	var b strings.Builder
	for _, t := range toks {
		fmt.Fprintf(&b, "%s %q\n", t, data[t.Offset:t.End()])
	}
	return stageDump{text: []byte(b.String()), diags: lex.Diagnostics()}
}

// dumpCST renders the lossless concrete syntax tree.
func dumpCST(data []byte) (stageDump, error) {
	doc := concrete.NewDocument(data)
	text, err := doc.Root().MarshalText()
	if err != nil {
		return stageDump{}, err
	}
	diags := append(append([]diagnostic.Diagnostic{}, doc.LexDiagnostics()...), doc.Diagnostics()...)
	return stageDump{text: text, diags: diags}, nil
}

// dumpAST renders the abstract syntax tree.
func dumpAST(data []byte) (stageDump, error) {
	doc := abstract.NewDocument(data)
	text, err := doc.File().MarshalText()
	if err != nil {
		return stageDump{}, err
	}
	diags := append(append([]diagnostic.Diagnostic{}, doc.Concrete().LexDiagnostics()...), doc.Diagnostics()...)
	return stageDump{text: text, diags: diags}, nil
}

// dumpIR runs the one-file analysis check runs and renders the resolved module.
// The diagnostics gate the dump (see DumpCmd); --stats reports the run's
// footprint, recorded before the gate so a broken file's is reported too.
func dumpIR(path string, data []byte) (stageDump, error) {
	doc := abstract.NewDocument(data)
	prog := semantic.NewProgram()
	id := semantic.FileID(displayPath(path))
	prog.SetFile(id, doc, nil)
	prog.Refresh()
	reportStats(prog.Stats(), 1, countDecls(doc.File()))

	text, err := prog.Module(id).MarshalText()
	if err != nil {
		return stageDump{}, err
	}
	return stageDump{text: text, diags: gatherDiagnostics(doc, prog, id), gate: true}, nil
}

// writeDump writes the stage's text to stdout, or — for --reporter=json — a JSON
// envelope keying the text under the stage name beside the file. The earlier
// stages' text and the IR alike are exact representations, so embedding them as
// a JSON string needs no further structure.
func writeDump(cmd *cobra.Command, stage, name string, text []byte) error {
	switch kind, _ := cmd.Flags().GetString("reporter"); kind {
	case reporterText:
		_, err := cmd.OutOrStdout().Write(text)
		return err
	case reporterJSON:
		out, err := json.MarshalIndent(map[string]any{"file": name, stage: string(text)}, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return err
	default:
		return fmt.Errorf("unknown reporter %q (want text or json)", kind)
	}
}
