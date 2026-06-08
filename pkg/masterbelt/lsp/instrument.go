// This file is the LSP server's performance instrumentation: a live pprof
// endpoint, coarse per-request latency spans, and a steady-memory + memo-table
// sampler. All three are off by default and add zero overhead when their env
// switches are unset — the constructor reads the switches once, and the hot
// paths short-circuit on a bool.
//
// Dependency note: otel would be the obvious choice for the request spans, but
// the repo is deliberately lean and the tracing backend is only "stdout/ログ"
// with query-loop tracing forbidden. Pulling in the OpenTelemetry SDK for
// coarse, flagged, log-only spans would add a heavy dependency tree for no gain,
// so this uses only the standard library: net/http/pprof for live profiling and
// slog for the per-request timing and the memory sampler. No new module
// dependency is added.

package lsp

import (
	"context"
	"log/slog"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/* on http.DefaultServeMux
	"os"
	"runtime"
	"time"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
)

// Environment switches. Each is off when unset, so the default LSP session pays
// nothing: no listener, no per-request timer, no sampler goroutine.
const (
	// envPprofAddr, when set to a listen address (e.g. "localhost:6060"), starts
	// net/http/pprof on that address for live, interactive profiling of the
	// resident server. The lsp subcommand's --pprof flag feeds this too.
	envPprofAddr = "MASTERBELT_PPROF_ADDR"
	// envTraceRequests, when set to any non-empty value, logs one coarse span per
	// LSP request: the method and its wall-clock duration, via slog. Coarse by
	// design — one span per request, never per query — so the request timing
	// stays cheap and the log readable.
	envTraceRequests = "MASTERBELT_TRACE_REQUESTS"
)

// sampleInterval is how often the steady-memory sampler logs heap and memo-table
// size when request tracing is on, to surface the leak signal. Coarse on purpose:
// the signal is monotonic growth over a long session, not fine-grained spikes.
const sampleInterval = 30 * time.Second

// instrumentation holds the server's performance switches, read once at
// construction. traceRequests gates both the per-request spans and the memory
// sampler; pprofAddr (when non-empty) is the live pprof listen address.
type instrumentation struct {
	traceRequests bool
	pprofAddr     string
}

// readInstrumentation snapshots the env switches once, so the hot paths test a
// bool instead of calling os.Getenv per request.
func readInstrumentation() instrumentation {
	return instrumentation{
		traceRequests: os.Getenv(envTraceRequests) != "",
		pprofAddr:     os.Getenv(envPprofAddr),
	}
}

// startPprof starts the live pprof HTTP endpoint on addr (typically a localhost
// port) in a background goroutine, when addr is non-empty. The pprof handlers
// are registered on http.DefaultServeMux by the blank import above. The
// ListenAndServe error is logged via slog rather than ignored (it returns only
// on a real bind/serve failure, e.g. the port is taken) so the operator learns
// the endpoint never came up.
//
// addr comes from the lsp --pprof flag or MASTERBELT_PPROF_ADDR; off by default,
// so a normal session opens no socket.
func startPprof(addr string) {
	if addr == "" {
		return
	}
	slog.Info("masterbelt lsp: starting pprof endpoint", "addr", addr)
	go func() {
		// ReadHeaderTimeout guards the localhost diagnostic listener against a
		// stalled client holding the connection open (gosec G114).
		srv := &http.Server{Addr: addr, ReadHeaderTimeout: 10 * time.Second}
		if err := srv.ListenAndServe(); err != nil {
			slog.Error("masterbelt lsp: pprof endpoint stopped", "addr", addr, "err", err)
		}
	}()
}

// timed runs fn, and when request tracing is on logs one coarse span — the LSP
// method and fn's wall-clock duration — via slog. When tracing is off it calls
// fn directly: no timer, no allocation, no log. This is the single
// DidChange→parse→analyze→respond span; the handlers route their bodies through
// it so the timing wraps in one place.
func (s *Server) timed(method string, fn func() error) error {
	if !s.instr.traceRequests {
		return fn()
	}
	start := time.Now()
	err := fn()
	slog.Info("masterbelt lsp: request", "method", method, "dur", time.Since(start), "err", err)
	return err
}

// startMemorySampler launches, when request tracing is on, a background
// goroutine that every sampleInterval logs the runtime heap (HeapAlloc) and the
// total memo-table size across the server's workspaces. Monotonic growth of
// either over a long session is the leak signal. The goroutine stops
// when ctx is done (the server is shutting down). When tracing is off it starts
// nothing.
func (s *Server) startMemorySampler(ctx context.Context) {
	if !s.instr.traceRequests {
		return
	}
	go func() {
		ticker := time.NewTicker(sampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sampleMemory()
			}
		}
	}()
}

// sampleMemory logs one steady-memory sample: the runtime heap and the summed
// memo-table size of every workspace. memoTotal locks the server only to read
// the workspaces, matching every other handler's discipline (the workspaces map
// is mutated under s.mu).
func (s *Server) sampleMemory() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	memos := s.memoTotal()
	slog.Info("masterbelt lsp: memory sample",
		"heapAlloc", ms.HeapAlloc, "heapObjects", ms.HeapObjects, "memos", memos)
}

// memoTotal sums the memo-table size across every distinct workspace program.
// It is a side-channel read (semantic.Program.MemoCount), so it perturbs nothing
// the engine memoizes. It dedupes by program pointer over both project
// workspaces (s.roots) and standalone ones (reachable only through open
// documents), so a project program shared by several open files counts once.
func (s *Server) memoTotal() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[*semantic.Program]bool{}
	total := 0
	add := func(prog *semantic.Program) {
		if prog == nil || seen[prog] {
			return
		}
		seen[prog] = true
		total += prog.MemoCount()
	}
	for _, ws := range s.roots {
		add(ws.prog)
	}
	for _, v := range s.open {
		add(v.ws.prog)
	}
	return total
}
