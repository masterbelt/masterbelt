package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/internal/belttest"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// fileURI builds the URI of a project file through the same conversion the
// server uses for unopened siblings, so the two stay symmetric.
func fileURI(root, name string) protocol.DocumentURI {
	return pathURI(filepath.Join(root, name))
}

// openOnDisk opens the named project file in the server with its on-disk text.
func openOnDisk(t *testing.T, s *Server, root, name string) protocol.DocumentURI {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	uri := fileURI(root, name)
	if err := s.DidOpen(context.Background(), &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, Text: string(text)},
	}); err != nil {
		t.Fatal(err)
	}
	return uri
}

const crossMainSrc = "use geo from \"geometry.belt\"\nuse { Unit } from \"geometry.belt\"\nconst start = geo.Origin\nconst step = Unit\n"

func crossProject(t *testing.T) (root string) {
	return belttest.WriteFiles(t, map[string]string{
		"masterbelt.toml": "entry = \"main.belt\"\n",
		"main.belt":       crossMainSrc,
		"geometry.belt":   "pub const Origin = 0\npub const Unit = 1\n",
	})
}

// TestProjectOpenResolvesImports: opening one file of a project loads its
// unopened siblings, so imports resolve and no false diagnostics appear.
func TestProjectOpenResolvesImports(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")

	v := s.open[uri]
	if v.ws.proj == nil {
		t.Fatal("main.belt was not placed in its project workspace")
	}
	if diags := v.Diagnostics(); len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none (imports resolve against the sibling)", diags)
	}
}

// TestCrossFileDefinition: go-to-definition on a namespace member and on a
// selectively imported name jumps into the sibling file.
func TestCrossFileDefinition(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]

	cases := []struct {
		name   string
		offset int
		// geometry.belt: "pub const Origin = 0\npub const Unit = 1\n"
		wantLine, wantStart, wantEnd int
	}{
		{"namespace member", strings.Index(crossMainSrc, "geo.Origin") + 4, 0, 10, 16},
		{"selective import reference", strings.LastIndex(crossMainSrc, "Unit"), 1, 10, 14},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locs, err := s.Definition(context.Background(), &protocol.DefinitionParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{URI: uri},
					Position:     toPosition(v.Buffer(), tc.offset),
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(locs) != 1 {
				t.Fatalf("got %d locations, want 1", len(locs))
			}
			if want := fileURI(root, "geometry.belt"); locs[0].URI != want {
				t.Errorf("URI = %q, want %q", locs[0].URI, want)
			}
			r := locs[0].Range
			if r.Start.Line != tc.wantLine || r.Start.Character != tc.wantStart || r.End.Character != tc.wantEnd {
				t.Errorf("range = %+v, want line %d cols %d..%d", r, tc.wantLine, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

// TestCrossFileHover: hovering a namespace member describes the imported
// constant — its type and folded value.
func TestCrossFileHover(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]

	hov, err := s.Hover(context.Background(), &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     toPosition(v.Buffer(), strings.Index(crossMainSrc, "geo.Origin")+4),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if hov == nil {
		t.Fatal("no hover on the namespace member")
	}
	for _, fragment := range []string{"Origin", "= 0"} {
		if !strings.Contains(hov.Contents.Value, fragment) {
			t.Errorf("hover = %q, want it to contain %q", hov.Contents.Value, fragment)
		}
	}
}

// TestCrossFileDiagnosticsUpdate: editing the exporter updates the importer's
// diagnostics — removing pub from Unit makes main.belt's selective import
// report not_exported.
func TestCrossFileDiagnosticsUpdate(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	mainURI := openOnDisk(t, s, root, "main.belt")
	geoURI := openOnDisk(t, s, root, "geometry.belt")

	err := s.DidChange(context.Background(), &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: geoURI},
		},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{
			{Text: "pub const Origin = 0\nconst Unit = 1\n"}, // Unit loses pub
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	v := s.open[mainURI]
	var codes []string
	for _, d := range v.Diagnostics() {
		codes = append(codes, string(d.Code))
	}
	joined := strings.Join(codes, ",")
	if !strings.Contains(joined, string(semantic.CodeNotExported)) {
		t.Errorf("main.belt codes = %s, want not_exported after the exporter edit", joined)
	}
}

// TestCrossFileReferences: find-references from the declaration reaches every
// file of the workspace — the declaration, the namespace member access, and
// the selectively imported reference.
func TestCrossFileReferences(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	mainURI := openOnDisk(t, s, root, "main.belt")
	geoURI := openOnDisk(t, s, root, "geometry.belt")

	// From Origin's declaration in geometry.belt (line 0, col 10).
	v := s.open[geoURI]
	locs, err := s.References(context.Background(), &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: geoURI},
			Position:     toPosition(v.Buffer(), strings.Index("pub const Origin", "Origin")),
		},
		Context: protocol.ReferenceContext{IncludeDeclaration: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	byURI := map[protocol.DocumentURI]int{}
	for _, l := range locs {
		byURI[l.URI]++
	}
	if byURI[geoURI] != 1 || byURI[mainURI] != 1 {
		t.Fatalf("references = %v, want the declaration in geometry.belt and the geo.Origin access in main.belt", byURI)
	}
}

// TestCrossFileRename: renaming an imported constant edits its declaration,
// the namespace member access, the selective import list, and the reference —
// across both files.
func TestCrossFileRename(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	mainURI := openOnDisk(t, s, root, "main.belt")
	geoURI := openOnDisk(t, s, root, "geometry.belt")

	// Rename Unit from its reference in main.belt (const step = Unit).
	v := s.open[mainURI]
	we, err := s.Rename(context.Background(), &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: mainURI},
			Position:     toPosition(v.Buffer(), strings.LastIndex(crossMainSrc, "Unit")),
		},
		NewName: "Step",
	})
	if err != nil {
		t.Fatal(err)
	}
	if we == nil {
		t.Fatal("rename returned nil")
	}
	if len(we.Changes[geoURI]) != 1 {
		t.Errorf("geometry edits = %v, want the declaration", we.Changes[geoURI])
	}
	// main.belt: the import-list name in use { Unit } and the reference —
	// leaving the import list behind would dangle it.
	if len(we.Changes[mainURI]) != 2 {
		t.Errorf("main edits = %v, want the import-list name and the Unit reference", we.Changes[mainURI])
	}
	for _, edits := range we.Changes {
		for _, e := range edits {
			if e.NewText != "Step" {
				t.Errorf("edit = %+v, want NewText Step", e)
			}
		}
	}
}

// TestCrossFileRenameMemberEditsOnlyTheName: renaming through geo.Origin must
// replace just "Origin", never the "geo." qualifier.
func TestCrossFileRenameMemberEditsOnlyTheName(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	mainURI := openOnDisk(t, s, root, "main.belt")
	v := s.open[mainURI]

	memberOffset := strings.Index(crossMainSrc, "geo.Origin") + 4
	we, err := s.Rename(context.Background(), &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: mainURI},
			Position:     toPosition(v.Buffer(), memberOffset),
		},
		NewName: "Zero",
	})
	if err != nil {
		t.Fatal(err)
	}
	if we == nil {
		t.Fatal("rename returned nil")
	}
	edits := we.Changes[mainURI]
	if len(edits) != 1 {
		t.Fatalf("main edits = %+v, want just the member name", edits)
	}
	r := edits[0].Range
	// "Origin" inside "const start = geo.Origin" on line 2: cols 18..24.
	if r.Start.Line != 2 || r.Start.Character != 18 || r.End.Character != 24 {
		t.Errorf("member edit range = %+v, want line 2 cols 18..24", r)
	}
}

// TestUsePathCompletion: completing inside a use path offers the project's
// sibling files, relative to the importing file.
func TestUsePathCompletion(t *testing.T) {
	root := belttest.WriteFiles(t, map[string]string{
		"masterbelt.toml": "entry = \"src/main.belt\"\n",
		"src/main.belt":   "use geo from \"g\"\n",
		"src/geo.belt":    "pub const G = 1\n",
		"lib/util.belt":   "pub const U = 2\n",
	})
	s := NewServer()
	uri := openOnDisk(t, s, root, filepath.Join("src", "main.belt"))
	v := s.open[uri]

	list, err := s.Completion(context.Background(), &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     toPosition(v.Buffer(), strings.Index("use geo from \"g\"", "\"g")+2),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, item := range list.Items {
		labels = append(labels, item.Label)
	}
	want := "../lib/util.belt,geo.belt"
	if got := strings.Join(labels, ","); got != want {
		t.Errorf("use-path completions = %s, want %s", got, want)
	}
}

// TestProjectlessFileStaysStandalone: a file outside any project analyzes
// alone, exactly as before.
func TestProjectlessFileStaysStandalone(t *testing.T) {
	dir := t.TempDir() // no masterbelt.toml anywhere above
	path := filepath.Join(dir, "lone.belt")
	if err := os.WriteFile(path, []byte("const A = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewServer()
	uri := openOnDisk(t, s, dir, "lone.belt")
	v := s.open[uri]
	if v.ws.proj != nil {
		t.Fatal("a projectless file must analyze standalone")
	}
	if diags := v.Diagnostics(); len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
}

// TestCrossFileRenameFromImportList: a rename may start on the import-list
// name itself (use { Unit }) and reaches the declaration and every reference.
func TestCrossFileRenameFromImportList(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	mainURI := openOnDisk(t, s, root, "main.belt")
	geoURI := openOnDisk(t, s, root, "geometry.belt")

	v := s.open[mainURI]
	we, err := s.Rename(context.Background(), &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: mainURI},
			Position:     toPosition(v.Buffer(), strings.Index(crossMainSrc, "{ Unit }")+2),
		},
		NewName: "Step",
	})
	if err != nil {
		t.Fatal(err)
	}
	if we == nil {
		t.Fatal("rename returned nil")
	}
	if len(we.Changes[geoURI]) != 1 || len(we.Changes[mainURI]) != 2 {
		t.Errorf("edits = geo %d / main %d, want 1 / 2", len(we.Changes[geoURI]), len(we.Changes[mainURI]))
	}
	for _, edits := range we.Changes {
		for i := 1; i < len(edits); i++ {
			a, b := edits[i-1].Range.Start, edits[i].Range.Start
			if a.Line > b.Line || (a.Line == b.Line && a.Character > b.Character) {
				t.Errorf("edits out of order: %+v before %+v", edits[i-1], edits[i])
			}
		}
	}
}

// TestDidCloseRevertsToDisk: closing a modified-but-unsaved exporter reverts
// it to the on-disk content, so the still-open importer's diagnostics stop
// reflecting the abandoned edits.
func TestDidCloseRevertsToDisk(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	mainURI := openOnDisk(t, s, root, "main.belt")
	geoURI := openOnDisk(t, s, root, "geometry.belt")

	// Unexport Unit in the buffer only — never saved.
	err := s.DidChange(context.Background(), &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: geoURI},
		},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{
			{Text: "pub const Origin = 0\nconst Unit = 1\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if diags := s.open[mainURI].Diagnostics(); len(diags) == 0 {
		t.Fatal("the unsaved edit must surface not_exported in the importer first")
	}

	// Closing the buffer abandons the edit: disk still says pub Unit.
	if err := s.DidClose(context.Background(), &protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: geoURI},
	}); err != nil {
		t.Fatal(err)
	}
	if diags := s.open[mainURI].Diagnostics(); len(diags) != 0 {
		t.Fatalf("importer diagnostics = %v, want none after the close reverts to disk", diags)
	}
}

// TestEditDropsOrphanedSibling: removing the last import of an unopened
// sibling drops it from the analyzed program on the very edit.
func TestEditDropsOrphanedSibling(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	mainURI := openOnDisk(t, s, root, "main.belt") // geometry.belt stays unopened
	v := s.open[mainURI]
	if n := len(v.ws.prog.Files()); n != 2 {
		t.Fatalf("program holds %d files, want main + geometry", n)
	}

	err := s.DidChange(context.Background(), &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: mainURI},
		},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{
			{Text: "const start = 1\n"}, // both use lines gone
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.ws.prog.Files(); len(got) != 1 || got[0] != v.id {
		t.Errorf("program files = %v, want just main.belt", got)
	}
}

// TestOpenFileSurvivesOrphaning: an edit that orphans a file the editor has
// open must not drop it — the open pins it until it closes.
func TestOpenFileSurvivesOrphaning(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	mainURI := openOnDisk(t, s, root, "main.belt")
	geoURI := openOnDisk(t, s, root, "geometry.belt")

	err := s.DidChange(context.Background(), &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: mainURI},
		},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{
			{Text: "const start = 1\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	v := s.open[mainURI]
	if n := len(v.ws.prog.Files()); n != 2 {
		t.Fatalf("program holds %d files, want the open geometry.belt kept", n)
	}
	// The orphaned-but-open file keeps its full view.
	geo := s.open[geoURI]
	if geo.AST() == nil || geo.Module() == nil {
		t.Error("the open orphan lost its analysis")
	}

	// Closing it releases the pin: now it leaves the program.
	if err := s.DidClose(context.Background(), &protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: geoURI},
	}); err != nil {
		t.Fatal(err)
	}
	if got := v.ws.prog.Files(); len(got) != 1 || got[0] != v.id {
		t.Errorf("program files after the close = %v, want just main.belt", got)
	}
}

// TestCloseKeepsImportedFile: closing a file the entry still imports keeps it
// in the program — only the pin goes, not the file.
func TestCloseKeepsImportedFile(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	mainURI := openOnDisk(t, s, root, "main.belt")
	geoURI := openOnDisk(t, s, root, "geometry.belt")

	if err := s.DidClose(context.Background(), &protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: geoURI},
	}); err != nil {
		t.Fatal(err)
	}
	v := s.open[mainURI]
	if n := len(v.ws.prog.Files()); n != 2 {
		t.Fatalf("program holds %d files, want geometry.belt kept while imported", n)
	}
	if diags := v.Diagnostics(); len(diags) != 0 {
		t.Errorf("main diagnostics = %v, want none", diags)
	}
}

// TestQualifiedTypeCompletion: a type position offers the namespace-qualified
// names of the file's namespace imports alongside the plain in-scope names.
func TestQualifiedTypeCompletion(t *testing.T) {
	src := "use geo from \"geometry.belt\"\nconst start: P = geo.Origin\n"
	root := belttest.WriteFiles(t, map[string]string{
		"masterbelt.toml": "entry = \"main.belt\"\n",
		"main.belt":       src,
		"geometry.belt":   "pub type Point = int32\npub const Origin = 0\n",
	})
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]

	// Type position: the cursor just past the partial annotation ": P".
	list, err := s.Completion(context.Background(), &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     toPosition(v.Buffer(), strings.Index(src, ": P")+3),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for _, item := range list.Items {
		labels[item.Label] = true
	}
	for _, want := range []string{"geo.Point", "int32"} {
		if !labels[want] {
			t.Errorf("type completion misses %q (got %d items)", want, len(list.Items))
		}
	}
}

// TestCrossFileTypeDefinition: go-to-definition and hover on a
// namespace-qualified type name reach the sibling file's declaration.
func TestCrossFileTypeDefinition(t *testing.T) {
	mainSrc := "use geo from \"geometry.belt\"\nconst p: geo.Point = 1\n"
	root := belttest.WriteFiles(t, map[string]string{
		"masterbelt.toml": "entry = \"main.belt\"\n",
		"main.belt":       mainSrc,
		"geometry.belt":   "/// a 1D point\npub type Point = int8\n",
	})
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]

	offset := strings.Index(mainSrc, "Point")
	locs := definition(v, offset)
	if len(locs) != 1 {
		t.Fatalf("got %d locations, want 1", len(locs))
	}
	if want := fileURI(root, "geometry.belt"); locs[0].URI != want {
		t.Errorf("URI = %q, want %q", locs[0].URI, want)
	}
	// geometry.belt line 1: `pub type Point = int8` — the name is cols 9..14.
	r := locs[0].Range
	if r.Start.Line != 1 || r.Start.Character != 9 || r.End.Character != 14 {
		t.Errorf("range = %+v, want line 1 cols 9..14", r)
	}

	h := hover(v, offset)
	if h == nil {
		t.Fatal("no hover on the qualified type name")
	}
	if !strings.Contains(h.Contents.Value, "pub type Point = int8") {
		t.Errorf("hover = %q, want the type signature", h.Contents.Value)
	}
	if !strings.Contains(h.Contents.Value, "a 1D point") {
		t.Errorf("hover = %q, want the doc comment", h.Contents.Value)
	}

	// The qualifier names a namespace, not a type: no hover claims it as one.
	if locs := definition(v, strings.Index(mainSrc, "geo.Point")); locs != nil {
		t.Errorf("definition(geo) = %v, want nil for the qualifier", locs)
	}
}

// TestCrossFileSemanticTokens: resolution-aware tokens — a namespace member
// access that names an imported constant renders as that constant, not as a
// property.
func TestCrossFileSemanticTokens(t *testing.T) {
	root := crossProject(t)
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]

	got := decode(semanticTokensIn(v).Data)
	// main.belt line 2: "const start = geo.Origin" — Origin at col 18.
	for _, tok := range got {
		if tok.line == 2 && tok.char == 18 {
			if tok.tokenType != stVariable || tok.mods != smReadonly {
				t.Errorf("geo.Origin token = %+v, want variable/readonly", tok)
			}
			return
		}
	}
	t.Fatalf("no token for Origin; got %+v", got)
}

// TestCrossFileTypeRename: renaming a type rewrites its declaration, its
// selective import, and every reference, across the project's files.
func TestCrossFileTypeRename(t *testing.T) {
	mainSrc := "use { Point } from \"geometry.belt\"\nconst p: Point = 1\n"
	root := belttest.WriteFiles(t, map[string]string{
		"masterbelt.toml": "entry = \"main.belt\"\n",
		"main.belt":       mainSrc,
		"geometry.belt":   "pub type Point = int8\nconst origin: Point = 0\n",
	})
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]

	edit := rename(v, strings.Index(mainSrc, ": Point")+3, "Spot")
	if edit == nil {
		t.Fatal("rename returned nil")
	}
	// main.belt: the use-list name and the annotation; geometry.belt: the
	// declaration and its local annotation reference.
	if got := len(edit.Changes[fileURI(root, "main.belt")]); got != 2 {
		t.Errorf("main.belt edits = %d, want 2 (use list + annotation)", got)
	}
	if got := len(edit.Changes[fileURI(root, "geometry.belt")]); got != 2 {
		t.Errorf("geometry.belt edits = %d, want 2 (declaration + reference)", got)
	}
}
