// Package cli implements the masterbelt command's subcommands: check, ir, and
// lsp. The cross-cutting profiling and stats flags live here on the root, not
// on any subcommand: CPU/heap/trace capture and the
// machine-readable --stats report are wanted from whichever subcommand runs,
// so they hang off RootCmd.PersistentFlags and are framed by the profiling
// lifecycle. Subcommands must NOT define their own persistent hooks — cobra
// runs only the nearest one, so a subcommand hook would shadow the root's.
//
// The lifecycle starts in PersistentPreRunE and ends in a cobra.OnFinalize
// callback, NOT PersistentPostRunE: cobra skips the post-run hooks when RunE
// (or the pre-run) returns an error, but OnFinalize fires after Execute
// regardless — so a profile or --stats report is still flushed for a `check`
// that exits non-zero on diagnostics (the case an operator most wants to
// profile), and a CPU profile started before a trace-open failure is still
// stopped.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"runtime/pprof"
	"runtime/trace"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
)

func init() {
	f := RootCmd.PersistentFlags()
	f.String("cpuprofile", "", "write a CPU profile to this file for the command's duration")
	f.String("memprofile", "", "write a heap profile to this file at exit")
	f.String("trace", "", "write an execution trace to this file for the command's duration")
	f.String("stats", "", "write the run's query/phase stats as JSON; bare --stats writes to stderr, --stats=PATH writes there")
	// NoOptDefVal lets bare --stats stand without swallowing the next argument
	// as its value; the sentinel routes to stderr, an explicit =PATH to a file.
	f.Lookup("stats").NoOptDefVal = statsStderr
	// --reporter is shared by the machine-readable subcommands (check, ir,
	// version); on the root so one definition serves them all and the logger can
	// honour it.
	f.String("reporter", reporterText, "diagnostic reporter for check, ir, and version: text or json")
	// OnFinalize runs after Execute whatever the outcome — the cleanup hook
	// that survives a RunE error (PersistentPostRunE does not).
	cobra.OnFinalize(finishProfiling)
}

// statsStderr is the --stats value standing for "write to stderr" — the
// NoOptDefVal a bare --stats takes, distinct from any real file path.
const statsStderr = "\x00stderr"

// configureLogger installs the run's default slog logger — the one initialized
// here rather than in main so the choice can read the run's flags. It logs at
// warning level to stderr, as JSON when the machine-readable --reporter=json is
// requested (so logs share the output's shape) and as text otherwise.
func configureLogger(cmd *cobra.Command) {
	opts := &slog.HandlerOptions{Level: slog.LevelWarn}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if kind, _ := cmd.Flags().GetString("reporter"); kind == reporterJSON {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

// RootCmd is the masterbelt root command every subcommand hangs off; main
// executes it after wiring the process context and logger.
var RootCmd = &cobra.Command{
	Use:               "masterbelt [subcommand]",
	Short:             "masterbelt is the toolchain for the masterbelt language",
	Long:              "masterbelt is the toolchain for the masterbelt language.\n\nRun a subcommand such as `masterbelt lsp` to start the language server.",
	SilenceUsage:      true,
	SilenceErrors:     true,
	PersistentPreRunE: prepareRun,
}

// prepareRun is the root's single pre-run hook. cobra runs exactly one
// PersistentPreRunE, so the two independent setup concerns — configuring the
// run's logger and starting its profiling captures — are sequenced here rather
// than entangled in one function.
func prepareRun(cmd *cobra.Command, args []string) error {
	configureLogger(cmd)
	return startProfiling(cmd, args)
}

// profileState holds the running command's profiling lifecycle: the open CPU
// and trace sinks and the destinations resolved from the flags at start, so
// the OnFinalize cleanup needs no *cobra.Command. It is process-global because
// the CLI runs one command per process; startProfiling resets it each run so a
// re-entrant Execute (a test) does not inherit a prior run's state.
var profileState struct {
	cpu       *os.File
	traceOut  *os.File
	memPath   string    // --memprofile destination, or "" if unset
	statsDest string    // "" = off, statsStderr = stderr, else a file path
	errOut    io.Writer // where a stderr stats report goes
	stats     *statsReport
}

// statsReport is the machine-readable shape of a run's work: the query-engine
// reuse profile plus the corpus size. Phase timings join it when
// the phase-timer instrumentation lands; the JSON shape is forward-compatible.
type statsReport struct {
	Queries semantic.Stats `json:"queries"`
	Files   int            `json:"files"`
	Decls   int            `json:"decls"`
}

// reportStats records the analyzed run's stats for the finalizer to write. A
// subcommand calls it after Refresh; the finalizer emits it only when --stats
// is set, so the call is cheap to leave in unconditionally.
func reportStats(s semantic.Stats, files, decls int) {
	profileState.stats = &statsReport{Queries: s, Files: files, Decls: decls}
}

// startProfiling resets the lifecycle state, resolves the flag destinations,
// and opens and begins the CPU and trace captures. The heap profile and stats
// are written by finishProfiling.
func startProfiling(cmd *cobra.Command, _ []string) error {
	profileState.cpu = nil
	profileState.traceOut = nil
	profileState.stats = nil
	profileState.memPath, _ = cmd.Flags().GetString("memprofile")
	profileState.errOut = cmd.ErrOrStderr()
	profileState.statsDest = ""
	if flag := cmd.Flags().Lookup("stats"); flag != nil && flag.Changed {
		profileState.statsDest = flag.Value.String()
	}

	if path, _ := cmd.Flags().GetString("cpuprofile"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("cpuprofile: %w", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			_ = f.Close()
			return fmt.Errorf("cpuprofile: %w", err)
		}
		profileState.cpu = f
	}
	if path, _ := cmd.Flags().GetString("trace"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("trace: %w", err)
		}
		if err := trace.Start(f); err != nil {
			_ = f.Close()
			return fmt.Errorf("trace: %w", err)
		}
		profileState.traceOut = f
	}
	return nil
}

// finishProfiling ends every capture, writes the heap profile and the stats
// report, and closes the sinks. It runs from cobra.OnFinalize, so it fires
// after Execute whatever the command returned — including a RunE error and a
// startProfiling error that left the CPU profiler running. Errors here are
// logged to stderr rather than returned: the finalizer has no way to influence
// the exit code, and the command's own error is the one that matters.
func finishProfiling() {
	if profileState.cpu != nil {
		pprof.StopCPUProfile() // flushes the profile; Close only frees the fd
		_ = profileState.cpu.Close()
		profileState.cpu = nil
	}
	if profileState.traceOut != nil {
		trace.Stop() // flushes the trace; Close only frees the fd
		_ = profileState.traceOut.Close()
		profileState.traceOut = nil
	}
	if profileState.memPath != "" {
		writeHeapProfile(profileState.memPath, profileState.errOut)
	}
	if err := writeStats(); err != nil {
		_, _ = fmt.Fprintf(profileState.errOut, "masterbelt: writing stats: %v\n", err)
	}
}

// writeHeapProfile materializes the live heap and writes it to path.
func writeHeapProfile(path string, errOut io.Writer) {
	f, err := os.Create(path)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "masterbelt: memprofile: %v\n", err)
		return
	}
	runtime.GC() // materialize the live heap before the snapshot
	writeErr := pprof.WriteHeapProfile(f)
	closeErr := f.Close()
	if writeErr != nil {
		_, _ = fmt.Fprintf(errOut, "masterbelt: memprofile: %v\n", writeErr)
	} else if closeErr != nil {
		_, _ = fmt.Fprintf(errOut, "masterbelt: memprofile: %v\n", closeErr)
	}
}

// writeStats emits the recorded stats as JSON when --stats was set and the
// subcommand produced a report. An empty flag value writes to stderr (stdout
// is the command's own output channel, e.g. check --reporter=json); a path
// writes there.
func writeStats() error {
	if profileState.statsDest == "" || profileState.stats == nil {
		return nil
	}
	doc, err := json.MarshalIndent(profileState.stats, "", "  ")
	if err != nil {
		return err
	}
	if profileState.statsDest != statsStderr {
		return os.WriteFile(profileState.statsDest, append(doc, '\n'), 0o644)
	}
	_, err = fmt.Fprintln(profileState.errOut, string(doc))
	return err
}
