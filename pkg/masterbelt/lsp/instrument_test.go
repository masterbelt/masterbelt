package lsp

import (
	"context"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
)

// TestInstrumentationOffByDefault is the off-by-default proof (D-1 §8-4): with
// neither env switch set, a server constructed by NewServer carries no
// instrumentation, so the request path and sampler short-circuit and a normal
// session pays nothing. (The env is unset in the test process by default; this
// pins that the switches read from it.)
func TestInstrumentationOffByDefault(t *testing.T) {
	t.Setenv(envTraceRequests, "")
	t.Setenv(envPprofAddr, "")
	s := NewServer()
	if s.instr.traceRequests {
		t.Error("traceRequests is on with MASTERBELT_TRACE_REQUESTS unset")
	}
	if s.instr.pprofAddr != "" {
		t.Errorf("pprofAddr is %q with MASTERBELT_PPROF_ADDR unset; want empty", s.instr.pprofAddr)
	}

	// startMemorySampler and startPprof are no-ops when off: they must not block
	// or panic when called on an off server.
	s.startMemorySampler(context.Background())
	startPprof(s.instr.pprofAddr)

	// timed runs the body and returns its error unchanged when tracing is off.
	called := false
	if err := s.timed("textDocument/didChange", func() error { called = true; return nil }); err != nil {
		t.Fatalf("timed returned %v, want nil", err)
	}
	if !called {
		t.Error("timed did not run the wrapped function")
	}
}

// TestInstrumentationReadsEnv pins that the switches reflect the env at
// construction, so --pprof / MASTERBELT_TRACE_REQUESTS turn the features on.
func TestInstrumentationReadsEnv(t *testing.T) {
	t.Setenv(envTraceRequests, "1")
	t.Setenv(envPprofAddr, "localhost:0")
	s := NewServer()
	if !s.instr.traceRequests {
		t.Error("traceRequests is off with MASTERBELT_TRACE_REQUESTS=1")
	}
	if s.instr.pprofAddr != "localhost:0" {
		t.Errorf("pprofAddr = %q, want localhost:0", s.instr.pprofAddr)
	}
}

// TestMemoTotal checks the memo-table sampler's read across workspaces: after a
// document is opened and analyzed, the summed memo count is non-zero, and an
// empty server reports zero. This exercises Server.memoTotal -> Program.MemoCount.
func TestMemoTotal(t *testing.T) {
	s := NewServer()
	if got := s.memoTotal(); got != 0 {
		t.Errorf("memoTotal on an empty server = %d, want 0", got)
	}

	ctx := context.Background()
	uri := protocol.DocumentURI("file:///x.belt")
	if err := s.DidOpen(ctx, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, Text: "const A = 1\nconst B = A\n"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.memoTotal(); got == 0 {
		t.Error("memoTotal after opening a file = 0; the analysis populated no memos")
	}
}
