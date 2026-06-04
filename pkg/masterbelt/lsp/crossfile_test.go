package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// writeProject lays a project on disk and returns its root: the editor's
// workspace loads unopened siblings from there.
func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func fileURI(root, name string) protocol.DocumentURI {
	return protocol.DocumentURI("file://" + filepath.ToSlash(filepath.Join(root, name)))
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
	return writeProject(t, map[string]string{
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
