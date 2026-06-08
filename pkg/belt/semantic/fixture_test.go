package semantic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// midsizeDir is the committed mid-size real fixture: a game-data
// project of several files with cross-file imports, modeling the language's
// purpose rather than the synthetic generator's shape. Its files share one
// directory, so a use path is the imported file's name — the flat resolution
// the program tests already use.
const midsizeDir = "../testdata/projects/midsize"

// loadFixtureDir reads every .belt file under dir (one level, the fixture's
// layout) into FileID-keyed documents.
func loadFixtureDir(t *testing.T, dir string) map[FileID]*abstract.Document {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fixture dir: %v", err)
	}
	docs := map[FileID]*abstract.Document{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".belt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		docs[FileID(e.Name())] = abstract.NewDocument(data)
	}
	return docs
}

// fixtureUses resolves a fixture's flat use graph: a use path is the imported
// file's name, omitted when it names no loaded file.
func fixtureUses(docs map[FileID]*abstract.Document) map[FileID]map[*ast.UseDecl]FileID {
	uses := make(map[FileID]map[*ast.UseDecl]FileID, len(docs))
	for id, doc := range docs {
		table := map[*ast.UseDecl]FileID{}
		for _, u := range doc.File().Uses {
			if _, ok := docs[FileID(u.Path)]; ok {
				table[u] = FileID(u.Path)
			}
		}
		uses[id] = table
	}
	return uses
}

// TestFixtureMidsizeAnalyzesClean is the mid-size fixture's contract: the
// committed game-data project must type-check and fold with zero diagnostics, so
// it stays a valid corpus and a real-shaped regression target. A diagnostic here
// means the fixture drifted from the language (or the language from the
// fixture).
func TestFixtureMidsizeAnalyzesClean(t *testing.T) {
	docs := loadFixtureDir(t, midsizeDir)
	if len(docs) < 5 {
		t.Fatalf("fixture has %d files, want at least 5", len(docs))
	}
	_, diags := AnalyzeProgram(docs, fixtureUses(docs))
	for id, ds := range diags {
		for _, d := range ds {
			t.Errorf("%s: [%s] %s", id, d.Code, d.Message)
		}
	}
}
