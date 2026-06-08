package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/concrete"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic/reporter"
	"github.com/masterbelt/masterbelt/pkg/source/formatter"
)

func init() {
	RootCmd.AddCommand(FmtCmd)
	FmtCmd.Flags().BoolP("write", "w", false, "write the result back to the source file instead of stdout")
	FmtCmd.Flags().Bool("check", false, "list the files that are not formatted and exit non-zero; write nothing")
	FmtCmd.Flags().Bool("diff", false, "print a unified diff of the formatting changes instead of the result")
	FmtCmd.Flags().String("stdin-filepath", "", "path to resolve .editorconfig against when formatting standard input")
}

// FmtCmd is the fmt subcommand: it formats masterbelt sources canonically.
// There is no style to configure — the only substrate a project may tune (the
// indent unit and line terminator) comes from its .editorconfig, resolved per
// file exactly as the language server resolves it, so the CLI and the editor
// agree byte for byte. The flags are modes, not styles: where the result goes
// (stdout, -w in place, --diff) or whether anything is wrong (--check).
var FmtCmd = &cobra.Command{
	Use:   "fmt [files...]",
	Short: "Format masterbelt sources canonically",
	Long: "Format masterbelt source into its single canonical spelling.\n\n" +
		"With no argument, fmt formats every file of the project found at or above the working directory. " +
		"A path argument is formatted directly; a directory is walked for .belt files; \"-\" reads from standard input.\n\n" +
		"By default the formatted text is written to stdout. -w writes it back to each file, --diff shows a unified diff, " +
		"and --check lists the files that are not yet formatted and exits non-zero without writing anything.\n\n" +
		"There are no style options: a project's .editorconfig decides the indent unit and line ending, nothing else.",
	Args: cobra.ArbitraryArgs,
	RunE: runFmt,
}

// fmtMode is the single mode a fmt run operates in, chosen by the flags.
type fmtMode int

const (
	fmtStdout fmtMode = iota // default: write the formatted text to stdout
	fmtWrite                 // -w: write it back to the source file
	fmtCheck                 // --check: list unformatted files, exit non-zero
	fmtDiff                  // --diff: print a unified diff
)

func runFmt(cmd *cobra.Command, args []string) error {
	write, _ := cmd.Flags().GetBool("write")
	check, _ := cmd.Flags().GetBool("check")
	diff, _ := cmd.Flags().GetBool("diff")
	stdinPath, _ := cmd.Flags().GetString("stdin-filepath")

	mode, err := resolveMode(write, check, diff)
	if err != nil {
		return err
	}

	run := &fmtRun{mode: mode, out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr()}

	// Standard input is a single stream, never written back, and resolves its
	// .editorconfig against --stdin-filepath (a pipe has no path of its own).
	switch {
	case len(args) == 1 && args[0] == "-":
		if mode == fmtWrite {
			return fmt.Errorf("cannot use -w/--write with standard input")
		}
		if err := run.stdin(cmd.InOrStdin(), stdinPath); err != nil {
			return err
		}
	case slices.Contains(args, "-"):
		return fmt.Errorf("cannot mix standard input (-) with file arguments")
	default:
		paths, err := fmtTargets(cmd, args)
		if err != nil {
			return err
		}
		for _, p := range paths {
			if err := run.file(p); err != nil {
				return err
			}
		}
	}

	if mode == fmtCheck && run.unformatted > 0 {
		return fmt.Errorf("%d file(s) not formatted", run.unformatted)
	}
	return nil
}

// resolveMode picks the run's mode from the flags, rejecting a combination of
// the mutually exclusive ones — each says something different about where the
// output goes, so at most one may be set.
func resolveMode(write, check, diff bool) (fmtMode, error) {
	set := 0
	for _, b := range []bool{write, check, diff} {
		if b {
			set++
		}
	}
	if set > 1 {
		return 0, fmt.Errorf("at most one of -w/--write, --check, and --diff may be set")
	}
	switch {
	case write:
		return fmtWrite, nil
	case check:
		return fmtCheck, nil
	case diff:
		return fmtDiff, nil
	default:
		return fmtStdout, nil
	}
}

// fmtTargets resolves the paths to format. With no argument it is the current
// project's files, shared with check through loadProject so the two commands
// see the same project. An explicit file is formatted directly; a directory is
// walked for .belt files.
func fmtTargets(cmd *cobra.Command, args []string) ([]string, error) {
	if len(args) == 0 {
		return projectFiles(cmd, ".")
	}
	var paths []string
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			found, err := beltFilesUnder(arg)
			if err != nil {
				return nil, err
			}
			paths = append(paths, found...)
		} else {
			paths = append(paths, arg)
		}
	}
	return paths, nil
}

// projectFiles returns the on-disk paths of every file in the project at or
// above dir, loaded through the same front door as check so a malformed
// manifest is reported the same way.
func projectFiles(cmd *cobra.Command, dir string) ([]string, error) {
	rep := reporter.NewText(cmd.ErrOrStderr(), diagnostic.DefaultLocale)
	proj, loadErr := loadProject(rep, dir, "")
	if err := rep.Flush(); err != nil {
		return nil, err
	}
	if loadErr != nil {
		return nil, loadErr
	}
	paths := make([]string, 0, len(proj.Files()))
	for _, f := range proj.Files() {
		paths = append(paths, f.Path)
	}
	return paths, nil
}

// beltFilesUnder walks dir and returns its .belt files in lexical order.
func beltFilesUnder(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".belt" {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

// fmtRun carries one invocation's mode, output streams, and the count of
// unformatted files --check has found.
type fmtRun struct {
	mode        fmtMode
	out, errOut io.Writer
	unformatted int
}

// file formats one source file and acts on the result per the run's mode.
func (r *fmtRun) file(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, broken := formatSource(src, formatter.Resolve(path, formatter.DefaultLayout))

	if r.mode == fmtWrite {
		// Never write a parse-error file back: formatting a broken tree can
		// drop the text the error stands on, so the safe move is to leave it.
		if broken {
			fmt.Fprintf(r.errOut, "masterbelt fmt: skipping %s: source has syntax errors\n", displayPath(path))
			return nil
		}
		if out != string(src) {
			return os.WriteFile(path, []byte(out), info.Mode().Perm())
		}
		return nil
	}
	return r.report(displayPath(path), src, out)
}

// stdin formats the standard input stream, resolving its layout against
// stdinPath when one was given (and labeling its diagnostics with that name).
func (r *fmtRun) stdin(in io.Reader, stdinPath string) error {
	src, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	layout := formatter.DefaultLayout
	name := "<stdin>"
	if stdinPath != "" {
		layout = formatter.Resolve(stdinPath, layout)
		name = displayPath(stdinPath)
	}
	out, _ := formatSource(src, layout)
	return r.report(name, src, out)
}

// report handles the read-only modes — stdout, --check, --diff — for a source
// named name whose original bytes are src and canonical form is out.
func (r *fmtRun) report(name string, src []byte, out string) error {
	switch r.mode {
	case fmtCheck:
		if out != string(src) {
			r.unformatted++
			_, err := fmt.Fprintln(r.out, name)
			return err
		}
		return nil
	case fmtDiff:
		if out != string(src) {
			_, err := io.WriteString(r.out, unifiedDiff(name, name, string(src), out))
			return err
		}
		return nil
	default: // fmtStdout
		_, err := io.WriteString(r.out, out)
		return err
	}
}

// formatSource is the formatting itself: parse to the lossless concrete tree
// (formatting needs no type-checking, so the light concrete document is enough)
// and render it canonically under layout. It also reports whether the source
// has a lexer or parser error, which gates the in-place write.
func formatSource(src []byte, layout formatter.Layout) (out string, broken bool) {
	doc := concrete.NewDocument(src)
	out = formatter.Format(doc.Buffer(), doc.Root(), layout)
	broken = anyError(doc.LexDiagnostics()) || anyError(doc.Diagnostics())
	return out, broken
}

// anyError reports whether any diagnostic is an error (warnings and below do
// not make a file unsafe to rewrite).
func anyError(ds []diagnostic.Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == diagnostic.Error {
			return true
		}
	}
	return false
}
