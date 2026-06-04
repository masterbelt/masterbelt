package cli

import (
	"fmt"
	"io"
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
			return checkSource(cmd.OutOrStdout(), target, data)
		}

		proj, err := loadProject(cmd.OutOrStdout(), target)
		if err != nil {
			return err
		}
		entry := proj.EntryFile()
		return checkSource(cmd.OutOrStdout(), entry.Path, entry.Data)
	},
}

// loadProject opens the project at or above dir, reporting the manifest's
// diagnostics when there are any. It is the project-opening front door shared
// by every project-scoped subcommand (check today, fmt when B-3 lands).
func loadProject(w io.Writer, dir string) (*project.Project, error) {
	proj, diags := project.Open(dir)
	if diags.Len() == 0 {
		return proj, nil
	}

	rep := reporter.New(w)
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
func checkSource(w io.Writer, path string, data []byte) error {
	doc := semantic.NewDocument(data)

	var raw []diagnostic.Diagnostic
	raw = append(raw, doc.AST().Concrete().LexDiagnostics()...)
	raw = append(raw, doc.AST().Diagnostics()...)
	raw = append(raw, doc.Diagnostics()...)

	rep := reporter.New(w)
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
