// Package cli implements the masterbelt command's subcommands: check, ir, and
// lsp. The cross-cutting profiling and stats flags live here on the root, not
// on any subcommand (D-1 §2, §8-7): CPU/heap/trace capture and the
// machine-readable --stats report are wanted from whichever subcommand runs,
// so they hang off RootCmd.PersistentFlags and start/stop in the root's
// Persistent{Pre,Post}RunE. Subcommands must NOT define their own persistent
// hooks — cobra runs only the nearest one, so a subcommand hook would silently
// shadow these.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"runtime/trace"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
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
}

// statsStderr is the --stats value standing for "write to stderr" — the
// NoOptDefVal a bare --stats takes, distinct from any real file path.
const statsStderr = "\x00stderr"

// RootCmd is the masterbelt root command every subcommand hangs off; main
// executes it after wiring the process context and logger. Its persistent
// hooks frame every subcommand run with the profiling/stats lifecycle.
var RootCmd = &cobra.Command{
	Use:               "masterbelt [subcommand]",
	Short:             "masterbelt is the toolchain for the masterbelt language",
	Long:              "masterbelt is the toolchain for the masterbelt language.\n\nRun a subcommand such as `masterbelt lsp` to start the language server.",
	SilenceUsage:      true,
	SilenceErrors:     true,
	PersistentPreRunE: startProfiling,
	PersistentPostRunE: func(cmd *cobra.Command, _ []string) error {
		return stopProfiling(cmd)
	},
}

// profileState holds the open profile sinks for the current command, closed in
// PersistentPostRunE. It is process-global because the CLI runs one command
// per process.
var profileState struct {
	cpu      *os.File
	traceOut *os.File
}

// runStats is the stats the running subcommand recorded, written out by
// stopProfiling when --stats is set. A subcommand calls reportStats once it
// has analyzed; nil means the subcommand produced no stats (e.g. lsp).
var runStats *statsReport

// statsReport is the machine-readable shape of a run's work: the query-engine
// reuse profile (D-1 M-reuse) plus the corpus size. Phase timings join it when
// the phase-timer instrumentation lands; the JSON shape is forward-compatible.
type statsReport struct {
	Queries semantic.Stats `json:"queries"`
	Files   int            `json:"files"`
	Decls   int            `json:"decls"`
}

// reportStats records the analyzed run's stats for the --stats writer. A
// subcommand calls it after Refresh; it is a no-op sink when --stats is unset
// (the writer simply finds the value and discards it), so the call is cheap to
// leave in unconditionally.
func reportStats(s semantic.Stats, files, decls int) {
	runStats = &statsReport{Queries: s, Files: files, Decls: decls}
}

// startProfiling opens the CPU and trace sinks named by the persistent flags
// and begins capture. The heap profile and stats are written at stop.
func startProfiling(cmd *cobra.Command, _ []string) error {
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

// stopProfiling ends capture, writes the heap profile and the stats report,
// and closes every sink. It runs in PersistentPostRunE, so it fires after the
// subcommand's RunE returns (including on a RunE error, which cobra still
// pairs with the post-run hook).
func stopProfiling(cmd *cobra.Command) error {
	if profileState.cpu != nil {
		pprof.StopCPUProfile() // flushes the profile; the Close only frees the fd
		_ = profileState.cpu.Close()
		profileState.cpu = nil
	}
	if profileState.traceOut != nil {
		trace.Stop() // flushes the trace; the Close only frees the fd
		_ = profileState.traceOut.Close()
		profileState.traceOut = nil
	}
	if path, _ := cmd.Flags().GetString("memprofile"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("memprofile: %w", err)
		}
		runtime.GC() // materialize the live heap before the snapshot
		writeErr := pprof.WriteHeapProfile(f)
		closeErr := f.Close()
		if writeErr != nil {
			return fmt.Errorf("memprofile: %w", writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("memprofile: %w", closeErr)
		}
	}
	return writeStats(cmd)
}

// writeStats emits the recorded stats as JSON when --stats is set. An empty
// flag value writes to stderr (stdout is the command's own output channel,
// e.g. check --format=json); a path writes there.
func writeStats(cmd *cobra.Command) error {
	flag := cmd.Flags().Lookup("stats")
	if flag == nil || !flag.Changed {
		return nil
	}
	if runStats == nil {
		return nil // the subcommand recorded nothing (e.g. lsp)
	}
	doc, err := json.MarshalIndent(runStats, "", "  ")
	if err != nil {
		return err
	}
	if path := flag.Value.String(); path != "" && path != statsStderr {
		return os.WriteFile(path, append(doc, '\n'), 0o644)
	}
	_, err = fmt.Fprintln(cmd.ErrOrStderr(), string(doc))
	return err
}
