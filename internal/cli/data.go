package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic/reporter"
	"github.com/masterbelt/masterbelt/pkg/master"
	"github.com/masterbelt/masterbelt/pkg/master/format/csv"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/project/config"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

func init() {
	RootCmd.AddCommand(DataCmd)
	DataCmd.Flags().String("locale", string(diagnostic.DefaultLocale), "message locale (en, ja)")
	DataCmd.Flags().String("profile", "", "manifest profile to read (default: the top-level profile)")
}

// DataCmd is the data subcommand: it reads every master's declared sources,
// coerces each row against the master's fields, and prints the typed tables —
// the first command to turn master data from syntax into values. It reports
// every read, coercion, and refinement problem with the source cell it came
// from, and exits nonzero when any is an error.
var DataCmd = &cobra.Command{
	Use:   "data [path]",
	Short: "Read and type-check a project's master data",
	Long: "Read every master's declared sources, coerce each cell to its field's type, and print the typed rows.\n\n" +
		"Each source's location resolves under the project root, beneath the [source.<format>] base path the manifest sets for that format. " +
		"A value that is not a valid one of its field's type, a row that violates a field's refinement, a missing column, or an unreadable source is reported at the source declaration, naming the cell it came from.",
	Args: cobra.MaximumNArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		target := "."
		if len(args) == 1 {
			target = args[0]
		}
		profile, _ := cmd.Flags().GetString("profile")
		rep, err := newReporter(cmd, cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		proj, err := loadProject(rep, target, profile)
		if err != nil {
			_ = rep.Flush()
			return err
		}

		runErr := dumpData(cmd, rep, proj)
		if err := rep.Flush(); err != nil {
			return err
		}
		if runErr != nil {
			return runErr
		}
		if n := rep.Errors(); n > 0 {
			return fmt.Errorf("%d error(s)", n)
		}
		return nil
	},
}

// dumpData analyzes the project, then for every master of every file reads its
// sources, coerces them, and prints the typed tables to stdout while reporting
// each source's diagnostics against the .belt file that declared it.
func dumpData(cmd *cobra.Command, rep reporter.Reporter, proj *project.Project) error {
	prog := semantic.NewProgram()
	for _, f := range proj.Files() {
		prog.SetFile(semantic.FileID(f.ID), f.AST, semantic.UsesOf(f.Uses))
	}
	prog.Refresh()

	reg := master.NewRegistry()
	reg.Register(csv.New())
	cfg := activeProfile(proj)

	files := append([]*project.File(nil), proj.Files()...)
	sort.Slice(files, func(i, j int) bool { return files[i].ID < files[j].ID })
	for _, f := range files {
		id := semantic.FileID(f.ID)
		module := prog.Module(id)
		if module == nil {
			continue
		}
		var diags []diagnostic.Diagnostic
		for _, def := range module.Types {
			if def.Master == nil || def.MasterSyntax == nil {
				continue
			}
			d, err := readMaster(cmd, prog, id, f.AST, def, reg, proj.Root, cfg)
			if err != nil {
				return err
			}
			diags = append(diags, d...)
		}
		if len(diags) > 0 {
			rep.Report(source.NewFile(displayPath(f.Path), f.Data), diags)
		}
	}
	return nil
}

// readMaster reads, coerces, and prints every source of one master, returning
// the diagnostics the reads produced. Each source is read on its own — merging
// several into one table is a later step — so a master listing two sources
// prints two tables.
func readMaster(cmd *cobra.Command, prog *semantic.Program, id semantic.FileID, doc *abstract.Document, def *ir.TypeDef, reg *master.Registry, root string, cfg config.ProfileConfig) ([]diagnostic.Diagnostic, error) {
	fields, ok := master.RowFields(def.Master.Row)
	if !ok {
		return nil, nil // a malformed row the engine already reported; nothing to read
	}
	var diags []diagnostic.Diagnostic
	for _, entry := range def.MasterSyntax.Sources {
		offset, width := locatorSpan(doc, entry)
		format, found := reg.Lookup(entry.Format)
		if !found {
			diags = append(diags, master.UnknownFormat(offset, width, entry.Format))
			continue
		}
		opts, optDiags := checkOptions(entry, format, offset, width)
		diags = append(diags, optDiags...)

		base := cfg.Source[entry.Format].BasePath
		spec := master.SourceSpec{
			Path:    filepath.Join(root, base, entry.Locator),
			Display: filepath.ToSlash(filepath.Join(base, entry.Locator)),
			Options: opts,
			Offset:  offset,
			Width:   width,
		}
		raw, readDiags := format.Read(spec)
		diags = append(diags, readDiags.Items()...)

		typed, coerceDiags := master.Coerce(raw, fields, spec)
		diags = append(diags, coerceDiags...)
		diags = append(diags, checkRefinements(typed, fields, spec, prog.EvalEnv(id))...)

		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s <- %s\n%s\n", def.Name, spec.Display, typed.String()); err != nil {
			return diags, err
		}
	}
	return diags, nil
}

// checkRefinements runs each refined field's predicate over its typed cells,
// reporting the cell a value that fails it came from. Coercion runs no
// predicate — it needs the engine's evaluator, which this layer reaches through
// the program's eval env — so this is where a where-clause range check fires.
func checkRefinements(typed master.Table, fields []ir.Field, spec master.SourceSpec, env eval.GraphEnv) []diagnostic.Diagnostic {
	byName := make(map[string]ir.Field, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	var diags []diagnostic.Diagnostic
	for _, row := range typed.Rows {
		for i, col := range typed.Columns {
			cell := row.Cells[i]
			if cell.Value == nil {
				continue // a coercion gap, already reported
			}
			named, ok := byName[col].Type.(*ir.Named)
			if !ok || named.Def == nil || named.Def.Where == nil {
				continue
			}
			v := eval.GraphPredicate(named.Def.Where, cell.Value, named.Def, env)
			if v == nil || v.Kind != ir.ConstBool || !v.Bool {
				diags = append(diags, master.CellRefinement(spec.Offset, spec.Width, spec.Display, cell.Origin.Row, cell.Origin.Col, col, cell.Value.String(), named.Def.Name))
			}
		}
	}
	return diags
}

// checkOptions validates a source entry's options against the format's specs and
// returns the ones it accepted, flattened to strings. An unknown key or a value
// of the wrong type is reported (and dropped); anything else passes through for
// the format to interpret.
func checkOptions(entry *ast.SourceEntry, format master.Format, offset, width int) (map[string]string, []diagnostic.Diagnostic) {
	opts := map[string]string{}
	lit, ok := entry.Options.(*ast.RecordLit)
	if !ok {
		return opts, nil // no options, or a malformed one the parser recovered
	}
	specs := make(map[string]master.OptionKind, len(format.OptionSpecs()))
	for _, s := range format.OptionSpecs() {
		specs[s.Name] = s.Kind
	}
	var diags []diagnostic.Diagnostic
	for _, field := range lit.Fields {
		want, known := specs[field.Name]
		if !known {
			diags = append(diags, master.UnknownOption(offset, width, format.Name(), field.Name))
			continue
		}
		value, got, ok := literalValue(field.Value)
		if !ok || got != want {
			diags = append(diags, master.OptionTypeMismatch(offset, width, field.Name, want.String()))
			continue
		}
		opts[field.Name] = value
	}
	return opts, diags
}

// literalValue reads a record-literal option value as its string form and kind,
// or false when it is not a scalar literal (an option must be a constant the
// declaration spells out, not a computed expression).
func literalValue(e ast.Expr) (string, master.OptionKind, bool) {
	switch l := e.(type) {
	case *ast.StringLit:
		return l.Value, master.OptionString, true
	case *ast.BoolLit:
		return strconv.FormatBool(l.Value), master.OptionBool, true
	case *ast.IntLit:
		return l.Text, master.OptionInt, true
	default:
		return "", 0, false
	}
}

// activeProfile is the profile config the project was opened with — a named one,
// or the manifest's top-level keys for the default.
func activeProfile(proj *project.Project) config.ProfileConfig {
	if proj.Profile == "" {
		return proj.Config.ProfileConfig
	}
	return proj.Config.Profiles[proj.Profile]
}

// locatorSpan is the byte span of a source entry's locator string in its .belt
// file — what a diagnostic about the source's data anchors to, since the data
// lives in a file the diagnostic model does not address. It falls back to the
// whole entry when the locator string is absent (a malformed entry).
func locatorSpan(doc *abstract.Document, entry *ast.SourceEntry) (int, int) {
	entryTree, ok := findGreen(doc.Concrete().Tree(), entry.Syntax())
	if !ok {
		return 0, 0
	}
	if tok, ok := firstToken(entryTree, token.String); ok {
		return tok.Offset(), tok.Width()
	}
	return entryTree.Offset(), entryTree.Width()
}

// findGreen returns the positioned tree for a green node, found by identity in
// the red tree.
func findGreen(root cst.Tree, target *cst.Node) (cst.Tree, bool) {
	if n, ok := root.Node(); ok && n == target {
		return root, true
	}
	for _, child := range root.Children() {
		if t, ok := findGreen(child, target); ok {
			return t, true
		}
	}
	return cst.Tree{}, false
}

// firstToken returns the first token of the given kind in t, in source order.
func firstToken(t cst.Tree, kind token.Kind) (cst.Tree, bool) {
	if k, ok := t.TokenKind(); ok && k == kind {
		return t, true
	}
	for _, child := range t.Children() {
		if found, ok := firstToken(child, kind); ok {
			return found, true
		}
	}
	return cst.Tree{}, false
}
