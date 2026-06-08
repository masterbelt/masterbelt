package beltgen

import (
	"fmt"
	"strings"
)

// rng is a tiny deterministic generator (splitmix64) seeded per file, so a
// file's content is a pure function of the project seed and the file index —
// no global rand, no clock.
type rng struct{ state uint64 }

func newRNG(seed int64, index int) *rng {
	// Mix the seed and index so different files of one project diverge while a
	// given (seed, index) is always identical.
	s := uint64(seed)*0x9E3779B97F4A7C15 + uint64(index)*0xD1B54A32D192ED03 //nolint:gosec // index is a small non-negative file ordinal
	return &rng{state: s + 0x2545F4914F6CDD1D}
}

func (r *rng) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// intn returns a deterministic value in [lo, hi].
func (r *rng) intn(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + int(r.next()%uint64(hi-lo+1)) //nolint:gosec // hi-lo+1 is a small positive span
}

// constName is a file's j-th own exported constant.
func constName(fileIndex, j int) string {
	return fmt.Sprintf("C%d_%d", fileIndex, j)
}

// writeDecls emits a file's own declarations: a mix of exported constants, a
// type, an enum, and a function, all chosen to type-check clean. The count is
// DeclsPerFile (at least one).
func (g *gen) writeDecls(b *strings.Builder, n *node) {
	r := newRNG(g.p.Seed, n.index)
	count := max(g.p.DeclsPerFile, 1)
	for j := range count {
		writeOneDecl(b, n.index, j, r)
	}
}

// writeOneDecl emits a single declaration, its kind chosen deterministically by
// position so a file carries a representative spread (constants dominate, with a
// type, an enum, and a function sprinkled in).
func writeOneDecl(b *strings.Builder, fileIndex, j int, r *rng) {
	switch j % 5 {
	case 1:
		writeArithConst(b, fileIndex, j, r)
	case 2:
		writeBoolConst(b, fileIndex, j, r)
	case 3:
		writeTypeDecl(b, fileIndex, j)
	case 4:
		writeFnDecl(b, fileIndex, j, r)
	default:
		writeIntConst(b, fileIndex, j, r)
	}
}

// writeIntConst emits a plain exported integer constant.
func writeIntConst(b *strings.Builder, fileIndex, j int, r *rng) {
	fmt.Fprintf(b, "pub const %s = %d\n", constName(fileIndex, j), r.intn(0, 1000))
}

// writeArithConst emits an exported constant folded from an arithmetic
// expression over the file's first constant. Index 0 is always a plain int
// const (writeOneDecl's default arm), so the reference is always well-typed and
// the fold always succeeds; this is what gives each file a same-file value
// dependency the early-cutoff measurement can ride. When this is itself the
// first declaration it is a literal instead.
func writeArithConst(b *strings.Builder, fileIndex, j int, r *rng) {
	name := constName(fileIndex, j)
	if j > 0 {
		fmt.Fprintf(b, "pub const %s = %s + %d\n", name, constName(fileIndex, 0), r.intn(1, 9))
		return
	}
	fmt.Fprintf(b, "pub const %s = %d\n", name, r.intn(0, 1000))
}

// writeBoolConst emits an exported boolean constant from a comparison.
func writeBoolConst(b *strings.Builder, fileIndex, j int, r *rng) {
	fmt.Fprintf(b, "pub const %s = %d < %d\n", constName(fileIndex, j), r.intn(0, 100), r.intn(0, 100))
}

// writeTypeDecl emits a nominal type aliasing an integer base — a real type
// declaration that participates in the type-defs query without needing a
// value.
func writeTypeDecl(b *strings.Builder, fileIndex, j int) {
	fmt.Fprintf(b, "pub type T%d_%d = int\n", fileIndex, j)
}

// writeFnDecl emits an exported arrow function over its single parameter, so a
// file carries a callable the function-symbol query indexes.
func writeFnDecl(b *strings.Builder, fileIndex, j int, r *rng) {
	fmt.Fprintf(b, "pub fn f%d_%d(x: nint): nint -> x * %d\n", fileIndex, j, r.intn(2, 9))
}
