package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
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

// loadProject opens the project at or above dir, printing the manifest's
// diagnostics when there are any. It is the project-opening front door shared
// by every project-scoped subcommand (check today, fmt when B-3 lands).
func loadProject(w io.Writer, dir string) (*project.Project, error) {
	proj, diags := project.Open(dir)
	if diags.Len() > 0 {
		printManifestDiagnostics(w, dir, diags.Items())
	}
	if diags.HasErrors() {
		return nil, fmt.Errorf("%s: %d error(s)", config.FileName, countErrors(diags.Items()))
	}
	return proj, nil
}

// checkSource runs the full pipeline over one file and prints its diagnostics
// — lexer, parser, and semantic, ordered by position, the same aggregation the
// LSP publishes. It fails when any diagnostic is an error.
func checkSource(w io.Writer, path string, data []byte) error {
	doc := semantic.NewDocument(data)

	var raw []diagnostic.Diagnostic
	raw = append(raw, doc.AST().Concrete().LexDiagnostics()...)
	raw = append(raw, doc.AST().Diagnostics()...)
	raw = append(raw, doc.Diagnostics()...)
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].Offset < raw[j].Offset })

	printDiagnostics(w, path, data, raw)
	if n := countErrors(raw); n > 0 {
		return fmt.Errorf("%s: %d error(s)", displayPath(path), n)
	}
	return nil
}

// printManifestDiagnostics prints diagnostics that are about the project
// manifest, anchored to the masterbelt.toml found at or above dir. A missing
// manifest has nothing to anchor to, so its diagnostics print bare.
func printManifestDiagnostics(w io.Writer, dir string, diags []diagnostic.Diagnostic) {
	if root, ok := project.FindRoot(dir); ok {
		path := filepath.Join(root, config.FileName)
		if src, err := os.ReadFile(path); err == nil {
			printDiagnostics(w, path, src, diags)
			return
		}
	}
	for _, d := range diags {
		fmt.Fprintln(w, d.String())
	}
}

// printDiagnostics prints diagnostics anchored to a file, one per line as
// "path:line:col: severity[code]: message", resolving each offset against the
// file's content.
func printDiagnostics(w io.Writer, path string, src []byte, diags []diagnostic.Diagnostic) {
	buf := source.NewFile(path, src)
	disp := displayPath(path)
	for _, d := range diags {
		pos := buf.Position(d.Offset)
		fmt.Fprintf(w, "%s:%d:%d: %s\n", disp, pos.Line, pos.Column, d)
	}
}

// displayPath renders path relative to the working directory when it lies
// beneath it, the way compilers conventionally report locations.
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

func countErrors(diags []diagnostic.Diagnostic) int {
	n := 0
	for _, d := range diags {
		if d.Severity == diagnostic.Error {
			n++
		}
	}
	return n
}
