package lsp

import (
	"reflect"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	"github.com/masterbelt/masterbelt/pkg/source"
)

// testView analyzes src as a standalone single-file program — the way the
// server treats a file outside any project — for driving the feature
// implementations directly.
func testView(src string) view {
	ws := &workspace{
		prog: semantic.NewProgram(),
		open: map[semantic.FileID]protocol.DocumentURI{},
	}
	id := semantic.FileID("test.belt")
	ws.prog.SetFile(id, abstract.NewDocument([]byte(src)), nil)
	ws.prog.Refresh()

	uri := protocol.DocumentURI("file:///test.belt")
	ws.open[id] = uri
	return view{ws: ws, id: id, uri: uri}
}

func TestTreesCachedPerParse(t *testing.T) {
	v := testView("const A = 1\n")

	t1 := v.Trees()
	t2 := v.Trees()
	if reflect.ValueOf(t1).Pointer() != reflect.ValueOf(t2).Pointer() {
		t.Error("two requests over one parse rebuilt the positioned trees")
	}

	// An edit re-parses; the next request sees a fresh index over the new
	// root, found by green identity.
	doc := v.AST()
	doc.Edit(source.Edit{Start: 10, End: 11, NewText: []byte("22")}) // 1 -> 22
	t3 := v.Trees()
	if reflect.ValueOf(t3).Pointer() == reflect.ValueOf(t1).Pointer() {
		t.Error("the edit did not invalidate the positioned-tree cache")
	}
	root := doc.Concrete().Tree()
	if got, ok := t3[root.Green()]; !ok || got.End() != root.End() {
		t.Errorf("fresh index does not cover the new root (ok=%v)", ok)
	}
}
