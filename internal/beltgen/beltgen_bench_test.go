package beltgen_test

import (
	"testing"

	"github.com/masterbelt/masterbelt/internal/beltgen"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// BenchmarkColdCompile measures a from-scratch whole-program analysis
// (semantic.AnalyzeProgram) of each generated size — the one-shot CLI compile
// path. Documents are parsed once outside the loop so the bench
// isolates analysis, not parsing; the sub-benchmark labels make the size curve
// visible. Run with -benchmem for the allocation gate.
func BenchmarkColdCompile(b *testing.B) {
	for _, p := range corpus {
		srcs := beltgen.Project(p)
		sdocs, suses := bridge(srcs)
		b.Run(label(p), func(b *testing.B) {
			coldRun(b, sdocs, suses)
		})
	}
}

// coldRun is the measured loop of BenchmarkColdCompile, extracted so the outer
// setup stays small and the hot path is a single, legible call.
func coldRun(b *testing.B, docs map[semantic.FileID]*abstract.Document, uses map[semantic.FileID]map[*ast.UseDecl]semantic.FileID) {
	b.Helper()
	b.ReportAllocs()
	for range b.N {
		semantic.AnalyzeProgram(docs, uses)
	}
}
