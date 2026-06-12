package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic/reporter"
	"github.com/masterbelt/masterbelt/pkg/master"
	"github.com/masterbelt/masterbelt/pkg/master/format/csv"
	"github.com/masterbelt/masterbelt/pkg/master/load"
	"github.com/masterbelt/masterbelt/pkg/project"
	"github.com/masterbelt/masterbelt/pkg/project/config"
	"github.com/masterbelt/masterbelt/pkg/source"
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
		// Diagnostics go to stdout, the way check emits them, so the final error
		// log (which main writes to stderr) never lands in the same stream — a
		// --reporter=json run leaves a single clean JSON document on stdout.
		rep, err := newReporter(cmd, cmd.OutOrStdout())
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

// dumpData drives the master data layer: it analyzes the project, asks the
// loader for each file's typed tables, prints them to stdout, and reports the
// loader's diagnostics against the .belt file that declared them. The data-layer
// work — reading, coercing, checking — lives in pkg/master/load; this only wires
// the project to it and renders the result.
func dumpData(cmd *cobra.Command, rep reporter.Reporter, proj *project.Project) error {
	prog := semantic.NewProgram()
	for _, f := range proj.Files() {
		prog.SetFile(semantic.FileID(f.ID), f.AST, semantic.UsesOf(f.Uses))
	}
	prog.Refresh()

	files := append([]*project.File(nil), proj.Files()...)
	sort.Slice(files, func(i, j int) bool { return files[i].ID < files[j].ID })

	// A master's resolved types can depend on any file in the project (a row type
	// imported from another), so one broken file makes every resolution suspect.
	// Gather every file's own diagnostics first; if the project does not check,
	// report them and read nothing — data is only loaded against a clean project.
	// (Lint is advisory, check's concern, and left out.)
	progDiags := make(map[*project.File][]diagnostic.Diagnostic, len(files))
	broken := false
	for _, f := range files {
		d := gatherDiagnostics(f.AST, prog, semantic.FileID(f.ID))
		progDiags[f] = d
		broken = broken || hasError(d)
	}
	if broken {
		for _, f := range files {
			if len(progDiags[f]) > 0 {
				rep.Report(source.NewFile(displayPath(f.Path), f.Data), progDiags[f])
			}
		}
		return nil
	}

	reg := master.NewRegistry()
	reg.Register(csv.New())
	bases := basePaths(activeProfile(proj))
	text := reporterKind(cmd) == reporterText
	for _, f := range files {
		loaded, diags := load.File(prog, semantic.FileID(f.ID), proj.Root, bases, reg)
		// The typed tables are a text rendering; under --reporter=json only the
		// diagnostics document is emitted, so the tables do not pollute it.
		if text {
			for _, l := range loaded {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s <- %s\n%s\n", l.Master, l.Display, l.Table.String()); err != nil {
					return err
				}
			}
		}
		if len(diags) > 0 {
			rep.Report(source.NewFile(displayPath(f.Path), f.Data), diags)
		}
	}
	return nil
}

// hasError reports whether any diagnostic is an error — the gate on whether the
// project is trustworthy enough to read data against.
func hasError(diags []diagnostic.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == diagnostic.Error {
			return true
		}
	}
	return false
}

// reporterKind is the --reporter value in effect for this run.
func reporterKind(cmd *cobra.Command) string {
	kind, _ := cmd.Flags().GetString("reporter")
	return kind
}

// activeProfile is the profile config the project was opened with — a named one,
// or the manifest's top-level keys for the default.
func activeProfile(proj *project.Project) config.ProfileConfig {
	if proj.Profile == "" {
		return proj.Config.ProfileConfig
	}
	return proj.Config.Profiles[proj.Profile]
}

// basePaths flattens a profile's per-format source settings to the base path
// each format resolves its locators under, the form the data loader reads. A
// portable backslash base path the manifest accepted is normalized to slashes
// here, so filepath.Join resolves it to real directories rather than looking for
// a literal "data\csv" on a platform where the backslash is not a separator.
func basePaths(cfg config.ProfileConfig) map[string]string {
	m := make(map[string]string, len(cfg.Source))
	for name, sc := range cfg.Source {
		m[name] = strings.ReplaceAll(sc.BasePath, "\\", "/")
	}
	return m
}
