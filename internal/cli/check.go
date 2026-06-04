package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic/reporter"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/project/config"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(CheckCmd)
	CheckCmd.Flags().String("format", "text", "output format: text or json")
	CheckCmd.Flags().String("locale", string(diagnostic.DefaultLocale), "message locale (en, ja)")
	CheckCmd.Flags().String("profile", "", "manifest profile to check (default: the top-level profile)")
}

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
	entry := proj.EntryFile()
	return checkSource(rep, entry.Path, entry.Data)
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
// publishes. It fails when any diagnostic is an error.
func checkSource(rep reporter.Reporter, path string, data []byte) error {
	doc := semantic.NewDocument(data)

	var raw []diagnostic.Diagnostic
	raw = append(raw, doc.AST().Concrete().LexDiagnostics()...)
	raw = append(raw, doc.AST().Diagnostics()...)
	raw = append(raw, doc.Diagnostics()...)

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
