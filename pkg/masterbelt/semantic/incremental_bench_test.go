package semantic

import (
	"testing"

	"github.com/masterbelt/masterbelt/internal/beltgen"
)

// BenchmarkIncremental measures the LSP-realistic inner loop: a single
// keystroke applied through the incremental Document.Edit path followed by a
// Refresh (D-1 M-inc). The program is built cold once outside the loop; only the
// edit and the re-analysis it triggers are timed, which is what early cutoff is
// supposed to keep cheap regardless of project size. Sub-benchmarks per
// generator size make the incremental curve visible (D-1 M-scale); -benchmem
// gives the per-edit allocation gate.
func BenchmarkIncremental(b *testing.B) {
	for _, p := range benchCorpus {
		srcs := genSources(p)
		entry := FileID(beltgen.EntryFile)
		b.Run(corpusLabel(p), func(b *testing.B) {
			incrementalRun(b, srcs, entry)
		})
	}
}

// incrementalRun is the measured loop, extracted so the setup stays small. Each
// iteration inserts one newline at the file's current end and refreshes; the
// edit grows the buffer, so the offset is recomputed from the live length every
// iteration rather than reused.
func incrementalRun(b *testing.B, srcs map[FileID][]byte, entry FileID) {
	b.Helper()
	r := newReplayer(srcs)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r.apply(entry, insertChar(r.length(entry), '\n'))
	}
}
