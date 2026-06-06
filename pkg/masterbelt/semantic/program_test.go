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

	if typ, eval := constInfo(p, "main.belt", "start"); typ != "nint" || eval != "0" {
		t.Errorf("start = %s / %s, want nint / 0", typ, eval)
	}
	if typ, eval := constInfo(p, "main.belt", "base"); typ != "nint" || eval != "1" {
		t.Errorf("base = %s / %s, want nint / 1", typ, eval)
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
		"geometry.belt": "pub type Point = int\npub const Origin: Point = 0\n",
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
	if typ, eval := constInfo(p, "main.belt", "a"); typ != "nint" || eval != "1" {
		t.Errorf("a = %s / %s, want nint / 1", typ, eval)
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
		"palette.belt": "pub const C = 2\npub type Shade = sbyte\n",
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
	if typ, eval := constInfo(p, "main.belt", "x"); typ != "nint" || eval != "2" {
		t.Errorf("x = %s / %s, want nint / 2", typ, eval)
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
	if typ, eval := constInfo(p, "main.belt", "ok"); typ != "nint" || eval != "10" {
		t.Errorf("ok = %s / %s, want nint / 10", typ, eval)
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

func TestProgramImportedTypeInMethodBody(t *testing.T) {
	// A type conversion inside a method body resolves an imported type the
	// same way an annotation does — int32-typed Meters(1) cannot be returned
	// as int8.
	p := buildProgram(map[string]string{
		"geometry.belt": "pub type Meters = int\n",
		"main.belt":     "use { Meters } from \"geometry.belt\"\npub type T = sbyte impl {\n  pub f(): sbyte {\n    return Meters(1)\n  }\n}\n",
	})
	findDiag(t, p, "main.belt", CodeTypeMismatch)
}

func TestProgramRepushUnchangedInputs(t *testing.T) {
	// Re-pushing every file unchanged — what the LSP workspace does after each
	// keystroke — must not open a new revision, or every file would be stamped
	// changed and the cross-file early cutoff lost.
	srcs := map[string]string{
		"a.belt": "pub const X = 1\n",
		"b.belt": "use { X } from \"a.belt\"\nconst Y = X\n",
	}
	p := buildProgram(srcs)
	rev := p.db.revision

	for id, doc := range p.docs {
		uses := map[*ast.UseDecl]FileID{}
		for _, u := range doc.File().Uses {
			if _, ok := p.docs[FileID(u.Path)]; ok {
				uses[u] = FileID(u.Path)
			}
		}
		p.SetFile(id, doc, uses)
	}
	p.Refresh()

	if p.db.revision != rev {
		t.Errorf("revision = %d, want %d (unchanged inputs are no-ops)", p.db.revision, rev)
	}
	if _, eval := constInfo(p, "b.belt", "Y"); eval != "1" {
		t.Errorf("Y = %s, want 1", eval)
	}
}

func TestProgramValueCutoffKeepsFnIdentity(t *testing.T) {
	// g's value is a function constant whose Fn literal lives in a.belt.
	// Re-parsing a.belt must propagate the new literal pointer into g's value
	// — a structurally identical function from a re-parsed file is a new fact,
	// and keeping the old one would leave g holding a detached tree.
	srcs := map[string]string{
		"a.belt":    "pub const f = fn(x: nint): nint { return x }\n",
		"main.belt": "use { f } from \"a.belt\"\nconst g = f\n",
	}
	p := buildProgram(srcs)

	doc := abstract.NewDocument([]byte("pub const f = fn(x: nint): nint { return x }\n"))
	p.SetFile("a.belt", doc, nil)
	p.Refresh()

	want, ok := doc.File().Decls[0].Value.(*ast.FuncLit)
	if !ok {
		t.Fatal("a.belt's f is not a function literal")
	}
	for _, c := range p.Module("main.belt").Consts {
		if c.Name != "g" {
			continue
		}
		if c.Eval == nil || c.Eval.Kind != ir.ConstFunc {
			t.Fatalf("g = %v, want a function constant", c.Eval)
		}
		if c.Eval.Fn != want {
			t.Error("g's function literal points into the detached old tree")
		}
		return
	}
	t.Fatal("main.belt declares no g")
}

func TestProgramQualifiedTypes(t *testing.T) {
	// geo.Point works in an annotation, a generic application, a type-decl
	// body, and a function-literal signature — and names the same definition
	// the selective import binds, so the two forms are one type.
	srcs := map[string]string{
		"geometry.belt": "pub type Point = int\npub type Opt<T> = T | null\npub const Origin = 0\n",
		"main.belt": "use geo from \"geometry.belt\"\nuse { Point } from \"geometry.belt\"\n" +
			"const start: geo.Point = geo.Origin\n" +
			"const same: Point = start\n" +
			"type MyOpt = geo.Opt<sbyte>\n" +
			"const f = fn(p: geo.Point): geo.Point { return p }\n",
	}
	p := buildProgram(srcs)
	assertClean(t, p, "main.belt")
	if typ, eval := constInfo(p, "main.belt", "start"); typ != "Point" || eval != "0" {
		t.Errorf("start = %s / %s, want Point / 0", typ, eval)
	}
	if typ, _ := constInfo(p, "main.belt", "same"); typ != "Point" {
		t.Errorf("same = %s, want Point (geo.Point and Point are one definition)", typ)
	}
}

func TestProgramQualifiedTypeUnknown(t *testing.T) {
	// A qualified name the target does not export — and a qualifier that
	// names no namespace — both report unknown_type with the full form.
	srcs := map[string]string{
		"geometry.belt": "pub type Point = int\n",
		"main.belt":     "use geo from \"geometry.belt\"\nconst a: geo.Bogus = 1\nconst b: bogus.Point = 2\n",
	}
	p := buildProgram(srcs)
	var named []string
	for _, d := range p.Diagnostics("main.belt") {
		if d.Code == CodeUnknownType {
			named = append(named, d.Message)
		}
	}
	if len(named) != 2 || !strings.Contains(named[0], "geo.Bogus") || !strings.Contains(named[1], "bogus.Point") {
		t.Errorf("unknown_type messages = %v, want geo.Bogus and bogus.Point", named)
	}
}

func TestProgramQualifiedTypeInMethodSignature(t *testing.T) {
	// Qualified names resolve in method signatures, and an unknown one in a
	// parameter is reported rather than becoming a silent type variable.
	srcs := map[string]string{
		"geometry.belt": "pub type Point = int\n",
		"main.belt": "use geo from \"geometry.belt\"\n" +
			"pub type W = sbyte impl {\n  pub f(p: geo.Point): geo.Point {\n    return p\n  }\n}\n",
	}
	assertClean(t, buildProgram(srcs), "main.belt")

	srcs["main.belt"] = "use geo from \"geometry.belt\"\n" +
		"pub type W = sbyte impl {\n  pub f(p: geo.Bogus): sbyte {\n    return 1\n  }\n}\n"
	findDiag(t, buildProgram(srcs), "main.belt", CodeUnknownType)
}

func TestProgramQualifiedTypeDanglingQualifier(t *testing.T) {
	// `geo.` is already a parse diagnostic; the semantic layer stays silent.
	srcs := map[string]string{
		"geometry.belt": "pub type Point = int\n",
		"main.belt":     "use geo from \"geometry.belt\"\nconst a: geo. = 1\n",
	}
	assertClean(t, buildProgram(srcs), "main.belt")
}

func TestProgramQualifiedTypeThroughReexport(t *testing.T) {
	// A namespace surfaces its target's re-exports: geo.Color reaches through
	// geometry's pub use into palette.
	srcs := map[string]string{
		"palette.belt":  "pub type Color = sbyte\n",
		"geometry.belt": "pub use { Color } from \"palette.belt\"\n",
		"main.belt":     "use geo from \"geometry.belt\"\nconst c: geo.Color = 1\n",
	}
	p := buildProgram(srcs)
	assertClean(t, p, "main.belt")
	if typ, _ := constInfo(p, "main.belt", "c"); typ != "Color" {
		t.Errorf("c = %s, want Color", typ)
	}
}

func TestProgramCyclicModuleThreeWay(t *testing.T) {
	// Every edge of a three-file cycle reports cyclic_module — the reachable
	// sets must be exact even when their computations interleave.
	p := buildProgram(map[string]string{
		"a.belt": "use b from \"b.belt\"\npub const A = 1\n",
		"b.belt": "use c from \"c.belt\"\npub const B = 2\n",
		"c.belt": "use a from \"a.belt\"\npub const C = 3\n",
	})
	for _, file := range []FileID{"a.belt", "b.belt", "c.belt"} {
		findDiag(t, p, file, CodeCyclicModule)
	}
}

func TestProgramCyclicModuleSelfImport(t *testing.T) {
	// A file importing itself is the smallest cycle.
	p := buildProgram(map[string]string{
		"a.belt": "use a from \"a.belt\"\npub const A = 1\n",
	})
	findDiag(t, p, "a.belt", CodeCyclicModule)
}

func TestProgramReachableEarlyCutoff(t *testing.T) {
	// Editing a file outside a use subgraph must not recompute that
	// subgraph's reachability.
	p := buildProgram(map[string]string{
		"a.belt": "pub const X = 1\n",
		"b.belt": "use { X } from \"a.belt\"\nconst Y = X\n",
		"c.belt": "pub const Z = 9\n",
	})

	p.SetFile("c.belt", abstract.NewDocument([]byte("pub const Z = 10\n")), nil)
	p.Refresh()

	for _, id := range []FileID{"a.belt", "b.belt"} {
		if p.db.computed[reachableKey(id)] {
			t.Errorf("reachability of %s was recomputed; the edit to c.belt must not reach it", id)
		}
	}
}

func TestProgramRefreshSkipsUnchangedFiles(t *testing.T) {
	// Re-assembly is per file and demand-verified: editing one file must
	// re-assemble it (and any file that read a changed fact), but a file
	// whose inputs and read facts are untouched is only re-verified.
	srcs := map[string]string{
		"a.belt": "pub const X = 1\n",
		"b.belt": "use { X } from \"a.belt\"\nconst Y = X\n",
		"c.belt": "pub const Z = 9\n",
	}
	p := buildProgram(srcs)

	p.SetFile("c.belt", abstract.NewDocument([]byte("pub const Z = 10\n")), nil)
	p.Refresh()

	if !p.db.computed[moduleKey("c.belt")] {
		t.Error("c.belt was not re-assembled after its own edit")
	}
	for _, id := range []FileID{"a.belt", "b.belt"} {
		if p.db.computed[moduleKey(id)] {
			t.Errorf("%s was re-assembled; the edit to c.belt must not reach it", id)
		}
	}
}

func TestProgramShellsStableAcrossRefresh(t *testing.T) {
	// An unedited declaration keeps its ir.Const identity across refreshes —
	// what lets an unchanged module be reused and cross-file references keep
	// pointing at the same objects.
	srcs := map[string]string{
		"a.belt": "pub const X = 1\n",
		"b.belt": "use { X } from \"a.belt\"\nconst Y = X\n",
	}
	p := buildProgram(srcs)
	before := p.Module("b.belt").Consts[0]

	p.SetFile("a.belt", abstract.NewDocument([]byte("pub const X = 2\n")), nil)
	p.Refresh()

	if after := p.Module("b.belt").Consts[0]; after != before {
		t.Error("b.belt's unedited declaration changed its ir.Const identity")
	}
	if _, eval := constInfo(p, "b.belt", "Y"); eval != "2" {
		t.Errorf("Y = %s, want the re-evaluated 2", eval)
	}
}

// --- cross-file functions ---------------------------------------------------------

func TestProgramCrossFileFunctionCall(t *testing.T) {
	p := buildProgram(map[string]string{
		"math.belt": "pub fn double(x: nint): nint -> x * 2\nfn hidden(): nint -> 0\n",
		"main.belt": "use { double } from \"math.belt\"\nconst A = double(21)\n",
	})
	assertClean(t, p, "math.belt")
	assertClean(t, p, "main.belt")
	if typ, eval := constInfo(p, "main.belt", "A"); typ != "nint" || eval != "42" {
		t.Errorf("A = %s / %s, want nint / 42", typ, eval)
	}
	// The FuncCall targets the very ir.Function the owning module publishes.
	call, ok := p.Module("main.belt").Consts[0].Value.(*ir.FuncCall)
	if !ok {
		t.Fatalf("A lowered to %T, want *ir.FuncCall", p.Module("main.belt").Consts[0].Value)
	}
	if call.Target != p.Module("math.belt").Funcs[0] {
		t.Errorf("call target = %p, want math's double", call.Target)
	}
}

func TestProgramWildcardFunctionImport(t *testing.T) {
	p := buildProgram(map[string]string{
		"math.belt": "pub fn double(x: nint): nint -> x * 2\n",
		"main.belt": "use * from \"math.belt\"\nconst A = double(2)\n",
	})
	assertClean(t, p, "main.belt")
	if _, eval := constInfo(p, "main.belt", "A"); eval != "4" {
		t.Errorf("A eval = %s, want 4", eval)
	}
}

func TestProgramNamespaceFunctionCall(t *testing.T) {
	p := buildProgram(map[string]string{
		"math.belt": "pub fn double(x: nint): nint -> x * 2\n",
		"main.belt": "use math from \"math.belt\"\nconst A = math.double(21)\n",
	})
	assertClean(t, p, "main.belt")
	if typ, eval := constInfo(p, "main.belt", "A"); typ != "nint" || eval != "42" {
		t.Errorf("A = %s / %s, want nint / 42", typ, eval)
	}
}

func TestProgramNamespaceFunctionCallInBodies(t *testing.T) {
	// A namespace function call works inside a method body and a function
	// body, through the same qualified lookup.
	p := buildProgram(map[string]string{
		"math.belt": "pub fn double(x: nint): nint -> x * 2\n",
		"main.belt": "use math from \"math.belt\"\n" +
			"fn quad(x: nint): nint -> math.double(math.double(x))\n" +
			"const A = quad(3)\n" +
			"pub type T = sbyte impl {\n  pub f(): nint {\n    return math.double(7)\n  }\n}\n",
	})
	assertClean(t, p, "main.belt")
	if _, eval := constInfo(p, "main.belt", "A"); eval != "12" {
		t.Errorf("A eval = %s, want 12", eval)
	}
}

func TestProgramImportedOverloadSet(t *testing.T) {
	// An imported name carries its whole overload set; the argument selects.
	p := buildProgram(map[string]string{
		"math.belt": "pub fn scale(x: nint): nint -> x * 10\npub fn scale(s: string): string -> s\n",
		"main.belt": "use { scale } from \"math.belt\"\nconst A = scale(4)\nconst B = scale(\"x\")\n",
	})
	assertClean(t, p, "main.belt")
	if _, eval := constInfo(p, "main.belt", "A"); eval != "40" {
		t.Errorf("A eval = %s, want 40", eval)
	}
	if _, eval := constInfo(p, "main.belt", "B"); eval != "\"x\"" {
		t.Errorf("B eval = %s, want \"x\"", eval)
	}
}

func TestProgramFunctionNotExported(t *testing.T) {
	// A private function neither imports selectively nor resolves through a
	// namespace.
	p := buildProgram(map[string]string{
		"math.belt": "fn hidden(): nint -> 0\n",
		"main.belt": "use { hidden } from \"math.belt\"\nuse math from \"math.belt\"\nconst A = math.hidden()\n",
	})
	findDiag(t, p, "main.belt", CodeNotExported)
	findDiag(t, p, "main.belt", CodeUnknownMember)
}

func TestProgramAmbiguousFunctionImport(t *testing.T) {
	// The same function name from two imports with distinct sets is ambiguous
	// at the call, like any other doubly-claimed name.
	p := buildProgram(map[string]string{
		"a.belt":    "pub fn f(): nint -> 1\n",
		"b.belt":    "pub fn f(): nint -> 2\n",
		"main.belt": "use { f } from \"a.belt\"\nuse { f } from \"b.belt\"\nconst A = f()\n",
	})
	findDiag(t, p, "main.belt", CodeAmbiguousImport)
}

func TestProgramLocalFunctionShadowsImport(t *testing.T) {
	p := buildProgram(map[string]string{
		"math.belt": "pub fn f(): nint -> 1\n",
		"main.belt": "use * from \"math.belt\"\nfn f(): nint -> 2\nconst A = f()\n",
	})
	assertClean(t, p, "main.belt")
	if _, eval := constInfo(p, "main.belt", "A"); eval != "2" {
		t.Errorf("A eval = %s, want 2 (the local function shadows the import)", eval)
	}
}

func TestProgramFunctionReexport(t *testing.T) {
	// A pub use re-exports a function through a barrel.
	p := buildProgram(map[string]string{
		"math.belt":   "pub fn double(x: nint): nint -> x * 2\n",
		"barrel.belt": "pub use * from \"math.belt\"\n",
		"main.belt":   "use { double } from \"barrel.belt\"\nconst A = double(5)\n",
	})
	assertClean(t, p, "main.belt")
	if _, eval := constInfo(p, "main.belt", "A"); eval != "10" {
		t.Errorf("A eval = %s, want 10", eval)
	}
}
