package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic/reporter"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/project/config"
	"github.com/masterbelt/masterbelt/pkg/source"
)

func init() {
	RootCmd.AddCommand(CheckCmd)
	CheckCmd.Flags().String("format", "text", "output format: text or json")
	CheckCmd.Flags().String("locale", string(diagnostic.DefaultLocale), "message locale (en, ja)")
	CheckCmd.Flags().String("profile", "", "manifest profile to check (default: the top-level profile)")
}

// CheckCmd is the check subcommand: it parses and type-checks a masterbelt
// project or a standalone file and reports every diagnostic, exiting nonzero
// when any is an error.
var CheckCmd = &cobra.Command{
	Use:   "check [path]",
	Short: "Parse and type-check a masterbelt project or file",
	Long: "Parse and type-check masterbelt source, reporting every diagnostic with its source position.\n\n" +
		"With no argument (or a directory), check finds masterbelt.toml at or above that directory and analyzes the project's entry point. " +
		"With a file, check analyzes just that file, without a project.",
	Args: cobra.MaximumNArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		target := "."
		if len(args) == 1 {
			target = args[0]
		}
		rep, err := newReporter(cmd)
		if err != nil {
			return err
		}
		profile, _ := cmd.Flags().GetString("profile")

		checkErr := runCheck(rep, target, profile)
		if err := rep.Flush(); err != nil {
			return err
		}
		return checkErr
	},
}

// newReporter builds the reporter the flags ask for: the format picks the
// implementation (text streams lines, json emits one document on Flush) and
// the locale picks the language messages render in.
func newReporter(cmd *cobra.Command) (reporter.Reporter, error) {
	format, _ := cmd.Flags().GetString("format")
	locale, _ := cmd.Flags().GetString("locale")
	switch format {
	case "text":
		return reporter.NewText(cmd.OutOrStdout(), diagnostic.Locale(locale)), nil
	case "json":
		return reporter.NewJSON(cmd.OutOrStdout(), diagnostic.Locale(locale)), nil
	default:
		return nil, fmt.Errorf("unknown format %q (want text or json)", format)
	}
}

// runCheck checks target — a project directory or an ad-hoc file — reporting
// every diagnostic through rep.
func runCheck(rep reporter.Reporter, target, profile string) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		// An explicit file is checked ad hoc, without a project.
		data, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		return checkSource(rep, target, data)
	}

	proj, err := loadProject(rep, target, profile)
	if err != nil {
		return err
	}
	return checkProject(rep, proj)
}

// checkProject analyzes every file of the project — the closure of the entry
// profile's imports — as one program, and reports each file's lexer, parser,
// and semantic diagnostics, files in id order.
func checkProject(rep reporter.Reporter, proj *project.Project) error {
	prog := semantic.NewProgram()
	for _, f := range proj.Files() {
		prog.SetFile(semantic.FileID(f.ID), f.AST, semantic.UsesOf(f.Uses))
	}
	prog.Refresh()

	for _, f := range proj.Files() {
		raw := make([]diagnostic.Diagnostic, 0, len(f.AST.Concrete().LexDiagnostics())+len(f.AST.Diagnostics()))
		raw = append(raw, f.AST.Concrete().LexDiagnostics()...)
		raw = append(raw, f.AST.Diagnostics()...)
		raw = append(raw, prog.Diagnostics(semantic.FileID(f.ID))...)
		rep.Report(source.NewFile(displayPath(f.Path), f.Data), raw)
	}
	if n := rep.Errors(); n > 0 {
		return fmt.Errorf("%d error(s)", n)
	}
	return nil
}

// loadProject opens the project at or above dir with the given profile ("" is
// the default), reporting the manifest's diagnostics when there are any. It is
// the project-opening front door shared by every project-scoped subcommand
// (check today, fmt when B-3 lands).
func loadProject(rep reporter.Reporter, dir, profile string) (*project.Project, error) {
	proj, diags := project.OpenProfile(dir, profile)
	if diags.Len() == 0 {
		return proj, nil
	}

	if file := manifestFile(dir); file != nil {
		rep.Report(file, diags.Items())
	} else {
		rep.ReportBare(diags.Items())
	}
	if n := rep.Errors(); n > 0 {
		return nil, fmt.Errorf("%s: %d error(s)", config.FileName, n)
	}
	return proj, nil
}

// manifestFile loads the manifest at or above dir so its diagnostics can be
// anchored to it, or nil when there is none to read (then there is nothing to
// anchor to).
func manifestFile(dir string) *source.File {
	root, ok := project.FindRoot(dir)
	if !ok {
		return nil
	}
	path := filepath.Join(root, config.FileName)
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return source.NewFile(displayPath(path), src)
}

// checkSource runs the full pipeline over one file and reports its
// diagnostics — lexer, parser, and semantic, the same aggregation the LSP
// publishes. A standalone file is a program of one file, exactly as the LSP
// treats a file outside any project. It fails when any diagnostic is an
// error.
func checkSource(rep reporter.Reporter, path string, data []byte) error {
	doc := abstract.NewDocument(data)
	prog := semantic.NewProgram()
	id := semantic.FileID(filepath.ToSlash(path))
	prog.SetFile(id, doc, nil)
	prog.Refresh()

	raw := make([]diagnostic.Diagnostic, 0, len(doc.Concrete().LexDiagnostics())+len(doc.Diagnostics()))
	raw = append(raw, doc.Concrete().LexDiagnostics()...)
	raw = append(raw, doc.Diagnostics()...)
	raw = append(raw, prog.Diagnostics(id)...)

	rep.Report(source.NewFile(displayPath(path), data), raw)
	if n := rep.Errors(); n > 0 {
		return fmt.Errorf("%s: %d error(s)", displayPath(path), n)
	}
	return nil
}

// displayPath renders path relative to the working directory when it lies
// beneath it, the way compilers conventionally report locations. Labeling is
// CLI policy; the reporter prints whatever name it is handed.
func displayPath(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
