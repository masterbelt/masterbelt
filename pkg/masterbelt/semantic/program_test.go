package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// buildProgram parses srcs and wires each file's use declarations by their
// literal path — the flat-namespace stand-in for the project layer's path
// resolution. A use whose path names no file in srcs stays unresolved.
func buildProgram(srcs map[string]string) *Program {
	p := NewProgram()
	docs := map[FileID]*abstract.Document{}
	for name, src := range srcs {
		docs[FileID(name)] = abstract.NewDocument([]byte(src))
	}
	for id, doc := range docs {
		uses := map[*ast.UseDecl]FileID{}
		for _, u := range doc.File().Uses {
			if _, ok := docs[FileID(u.Path)]; ok {
				uses[u] = FileID(u.Path)
			}
		}
		p.SetFile(id, doc, uses)
	}
	p.Refresh()
	return p
}

// constInfo returns a module constant's type and folded value as strings, for
// compact assertions ("<nil>" when unevaluated).
func constInfo(p *Program, file FileID, name string) (typ, eval string) {
	for _, c := range p.Module(file).Consts {
		if c.Name == name {
			typ = c.Type.String()
			if c.Eval != nil {
				return typ, c.Eval.String()
			}
			return typ, "<nil>"
		}
	}
	return "<missing>", "<missing>"
}

// codesOf flattens a file's diagnostic codes for compact assertions.
func codesOf(p *Program, file FileID) string {
	var out []string
	for _, d := range p.Diagnostics(file) {
		out = append(out, string(d.Code))
	}
	return strings.Join(out, ",")
}

func assertClean(t *testing.T, p *Program, file FileID) {
	t.Helper()
	if diags := p.Diagnostics(file); len(diags) != 0 {
		t.Fatalf("%s diagnostics = %v, want none", file, diags)
	}
}

func findDiag(t *testing.T, p *Program, file FileID, code diagnostic.Code) diagnostic.Diagnostic {
	t.Helper()
	for _, d := range p.Diagnostics(file) {
		if d.Code == code {
			return d
		}
	}
	t.Fatalf("%s: no %s diagnostic; got [%s]", file, code, codesOf(p, file))
	return diagnostic.Diagnostic{}
}

func TestProgramCrossFileValues(t *testing.T) {
	p := buildProgram(map[string]string{
		"geometry.belt": "pub const Origin = 0\npub const Unit = 1\nconst hidden = 2\n",
		"main.belt":     "use geo from \"geometry.belt\"\nuse { Origin } from \"geometry.belt\"\nconst start = geo.Origin\nconst base = Origin.add(geo.Unit)\n",
	})
	assertClean(t, p, "main.belt")
	assertClean(t, p, "geometry.belt")

	if typ, eval := constInfo(p, "main.belt", "start"); typ != "int" || eval != "0" {
		t.Errorf("start = %s / %s, want int / 0", typ, eval)
	}
	if typ, eval := constInfo(p, "main.belt", "base"); typ != "int" || eval != "1" {
		t.Errorf("base = %s / %s, want int / 1", typ, eval)
	}
}

func TestProgramCrossFileReferenceTargets(t *testing.T) {
	// A cross-file ir.Reference points at the very ir.Const the owning module
	// publishes — the IR is one pointer graph.
	p := buildProgram(map[string]string{
		"geometry.belt": "pub const Origin = 0\n",
		"main.belt":     "use { Origin } from \"geometry.belt\"\nconst start = Origin\n",
	})
	origin := p.Module("geometry.belt").Consts[0]
	start := p.Module("main.belt").Consts[0]
	ref, ok := start.Value.(*ir.Reference)
	if !ok {
		t.Fatalf("start lowered to %T, want *ir.Reference", start.Value)
	}
	if ref.Target != origin {
		t.Errorf("reference target = %p (%s), want geometry's Origin %p", ref.Target, ref.Target.Name, origin)
	}
}

func TestProgramCrossFileTypeAnnotation(t *testing.T) {
	// An imported type works in a const annotation; the exporter's settled
	// type and value cross with it.
	p := buildProgram(map[string]string{
		"geometry.belt": "pub type Point = int32\npub const Origin: Point = 0\n",
		"main.belt":     "use geo from \"geometry.belt\"\nuse { Point } from \"geometry.belt\"\nconst start: Point = geo.Origin\n",
	})
	assertClean(t, p, "geometry.belt")
	assertClean(t, p, "main.belt")
	if typ, eval := constInfo(p, "main.belt", "start"); typ != "Point" || eval != "0" {
		t.Errorf("start = %s / %s, want Point / 0", typ, eval)
	}
}

func TestProgramWildcardImport(t *testing.T) {
	p := buildProgram(map[string]string{
		"geometry.belt": "pub const Origin = 0\npub const Unit = 1\nconst hidden = 2\n",
		"main.belt":     "use * from \"geometry.belt\"\nconst a = Origin.add(Unit)\nconst b = hidden\n",
	})
	if typ, eval := constInfo(p, "main.belt", "a"); typ != "int" || eval != "1" {
		t.Errorf("a = %s / %s, want int / 1", typ, eval)
	}
	// hidden is not pub: the wildcard does not bring it in.
	d := findDiag(t, p, "main.belt", CodeUndefinedName)
	if !strings.Contains(d.Message, "hidden") {
		t.Errorf("Message = %q, want it to name hidden", d.Message)
	}
}

func TestProgramBarrelReexport(t *testing.T) {
	// barrel re-exports palette's whole surface; main reaches it selectively
	// and through a wildcard of the barrel (transitively).
	p := buildProgram(map[string]string{
		"palette.belt": "pub const C = 2\npub type Shade = int8\n",
		"barrel.belt":  "pub use * from \"palette.belt\"\npub const Own = 1\n",
		"main.belt":    "use { C, Own } from \"barrel.belt\"\nuse * from \"barrel.belt\"\nconst x: Shade = C.add(Own)\n",
	})
	assertClean(t, p, "main.belt")
	if typ, eval := constInfo(p, "main.belt", "x"); typ != "Shade" || eval != "3" {
		t.Errorf("x = %s / %s, want Shade / 3", typ, eval)
	}
}

func TestProgramSelectiveReexport(t *testing.T) {
	p := buildProgram(map[string]string{
		"palette.belt": "pub const C = 2\npub const D = 3\n",
		"barrel.belt":  "pub use { C } from \"palette.belt\"\n",
		"main.belt":    "use { C } from \"barrel.belt\"\nuse { D } from \"barrel.belt\"\nconst x = C\n",
	})
	// C re-exports; D does not (the barrel re-exported only C).
	if typ, eval := constInfo(p, "main.belt", "x"); typ != "int" || eval != "2" {
		t.Errorf("x = %s / %s, want int / 2", typ, eval)
	}
	d := findDiag(t, p, "main.belt", CodeNotExported)
	if !strings.Contains(d.Message, "D") {
		t.Errorf("Message = %q, want it to name D", d.Message)
	}
}

func TestProgramUseNotFound(t *testing.T) {
	p := buildProgram(map[string]string{
		"main.belt": "use ghost from \"missing.belt\"\nconst a = 1\n",
	})
	d := findDiag(t, p, "main.belt", CodeUseNotFound)
	if !strings.Contains(d.Message, "missing.belt") {
		t.Errorf("Message = %q, want it to name the path", d.Message)
	}
}

func TestProgramNotExported(t *testing.T) {
	p := buildProgram(map[string]string{
		"geometry.belt": "pub const Origin = 0\nconst hidden = 1\n",
		"main.belt":     "use { hidden } from \"geometry.belt\"\nconst a = 1\n",
	})
	d := findDiag(t, p, "main.belt", CodeNotExported)
	for _, fragment := range []string{"hidden", "geometry.belt"} {
		if !strings.Contains(d.Message, fragment) {
			t.Errorf("Message = %q, want it to contain %q", d.Message, fragment)
		}
	}
}

func TestProgramUnknownMember(t *testing.T) {
	p := buildProgram(map[string]string{
		"geometry.belt": "pub const Origin = 0\nconst hidden = 1\n",
		"main.belt":     "use geo from \"geometry.belt\"\nconst a = geo.Nope\nconst b = geo.hidden\n",
	})
	var got []string
	for _, d := range p.Diagnostics("main.belt") {
		if d.Code == CodeUnknownMember {
			got = append(got, d.Message)
		}
	}
	if len(got) != 2 {
		t.Fatalf("unknown_member diagnostics = %v, want 2 (Nope and the private hidden)", got)
	}
	if !strings.Contains(got[0], "Nope") || !strings.Contains(got[1], "hidden") {
		t.Errorf("messages = %v", got)
	}
}

func TestProgramAmbiguousImport(t *testing.T) {
	srcs := map[string]string{
		"a.belt":    "pub const X = 1\npub const OnlyA = 10\n",
		"b.belt":    "pub const X = 2\n",
		"main.belt": "use * from \"a.belt\"\nuse * from \"b.belt\"\nconst y = X\nconst ok = OnlyA\n",
	}
	p := buildProgram(srcs)

	// X arrives from both wildcards: using it is ambiguous, OnlyA is fine.
	d := findDiag(t, p, "main.belt", CodeAmbiguousImport)
	if !strings.Contains(d.Message, "X") {
		t.Errorf("Message = %q, want it to name X", d.Message)
	}
	if typ, eval := constInfo(p, "main.belt", "ok"); typ != "int" || eval != "10" {
		t.Errorf("ok = %s / %s, want int / 10", typ, eval)
	}

	// Unused, the collision is harmless.
	srcs["main.belt"] = "use * from \"a.belt\"\nuse * from \"b.belt\"\nconst ok = OnlyA\n"
	assertClean(t, buildProgram(srcs), "main.belt")

	// A local declaration shadows the imports entirely.
	srcs["main.belt"] = "use * from \"a.belt\"\nuse * from \"b.belt\"\nconst X = 9\nconst y = X\n"
	p = buildProgram(srcs)
	assertClean(t, p, "main.belt")
	if _, eval := constInfo(p, "main.belt", "y"); eval != "9" {
		t.Errorf("y = %s, want the local 9", eval)
	}

	// The same declaration arriving twice (selective + wildcard of the same
	// file) is not a conflict.
	srcs["main.belt"] = "use { X } from \"a.belt\"\nuse * from \"a.belt\"\nconst y = X\n"
	p = buildProgram(srcs)
	assertClean(t, p, "main.belt")
}

func TestProgramCyclicModule(t *testing.T) {
	p := buildProgram(map[string]string{
		"a.belt": "use b from \"b.belt\"\npub const A = 1\n",
		"b.belt": "use a from \"a.belt\"\npub const B = 2\n",
	})
	for _, file := range []FileID{"a.belt", "b.belt"} {
		findDiag(t, p, file, CodeCyclicModule)
	}
}

func TestProgramEarlyCutoffCrossFile(t *testing.T) {
	// Editing a's exported constant re-analyzes its importer b, but a file
	// that imports nothing from a is left untouched — the cross-file version
	// of the early-cutoff guarantee.
	p := buildProgram(map[string]string{
		"a.belt": "pub const X = 1\n",
		"b.belt": "use { X } from \"a.belt\"\nconst Y = X\n",
		"c.belt": "pub const Z = 9\n",
	})
	zDecl := p.Document("c.belt").File().Decls[0]

	p.SetFile("a.belt", abstract.NewDocument([]byte("pub const X = 2\n")), nil)
	p.Refresh()

	if _, eval := constInfo(p, "b.belt", "Y"); eval != "2" {
		t.Errorf("Y = %s, want the new 2", eval)
	}
	if p.db.computed[typeOfKey(zDecl)] || p.db.computed[valueKey(zDecl)] {
		t.Error("c.belt was re-analyzed; the edit to a.belt must not reach it")
	}
}

func TestProgramIncrementalCrossFile(t *testing.T) {
	// Editing an exported constant's body re-evaluates its importers; the
	// settled type is unchanged, so importer types survive untouched.
	srcs := map[string]string{
		"geometry.belt": "pub const Origin = 0\n",
		"main.belt":     "use { Origin } from \"geometry.belt\"\nconst start = Origin\n",
	}
	p := buildProgram(srcs)
	if _, eval := constInfo(p, "main.belt", "start"); eval != "0" {
		t.Fatalf("start = %s, want 0", eval)
	}

	// Replace geometry's source wholesale (the project layer would re-parse).
	doc := abstract.NewDocument([]byte("pub const Origin = 5\n"))
	p.SetFile("geometry.belt", doc, nil)
	p.Refresh()
	if _, eval := constInfo(p, "main.belt", "start"); eval != "5" {
		t.Errorf("start after edit = %s, want 5", eval)
	}
	assertClean(t, p, "main.belt")
}
