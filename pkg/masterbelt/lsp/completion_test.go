package lsp

import (
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
)

// completionSrc has a documented, annotated constant and a second constant whose
// initializer is a value position; "int64" is the only type position.
const completionSrc = "/// the maximum\nconst Max: int64 = 100\nconst Cur = Max\n"

func byLabel(items []protocol.CompletionItem) map[string]protocol.CompletionItem {
	out := map[string]protocol.CompletionItem{}
	for _, it := range items {
		out[it.Label] = it
	}
	return out
}

func TestCompletionInValuePosition(t *testing.T) {
	doc := testView(completionSrc)

	// Inside the "Max" reference in "const Cur = Max" — a value position.
	offset := strings.Index(completionSrc, "= Max") + 3
	items := completion(doc, offset).Items
	got := byLabel(items)

	for _, want := range []string{"Max", "Cur", "true", "false", "null", "fn"} {
		if _, ok := got[want]; !ok {
			t.Errorf("value completion missing %q", want)
		}
	}

	// fn begins a function literal and is offered as a value keyword.
	if k := got["fn"].Kind; k == nil || *k != protocol.CompletionItemKindKeyword {
		t.Errorf("fn kind = %v, want Keyword", k)
	}

	// A constant carries its inferred type as detail and its doc comment.
	if d := got["Max"].Detail; d != ": int64" {
		t.Errorf("Max detail = %q, want %q", d, ": int64")
	}
	if doc := got["Max"].Documentation; doc == nil || !strings.Contains(doc.Value, "maximum") {
		t.Errorf("Max documentation = %v, want the doc comment", got["Max"].Documentation)
	}
	if k := got["Max"].Kind; k == nil || *k != protocol.CompletionItemKindConstant {
		t.Errorf("Max kind = %v, want Constant", k)
	}
}

func TestCompletionInTypePosition(t *testing.T) {
	doc := testView(completionSrc)

	// Inside the "int64" annotation — a type position. Type names are offered,
	// constant names are not.
	offset := strings.Index(completionSrc, "int64") + 2
	got := byLabel(completion(doc, offset).Items)

	for _, want := range []string{"int64", "bool", "string"} {
		if _, ok := got[want]; !ok {
			t.Errorf("type completion missing builtin %q", want)
		}
	}
	if _, ok := got["Max"]; ok {
		t.Error("type completion offered the constant Max")
	}
	if k := got["int64"].Kind; k == nil || *k != protocol.CompletionItemKindStruct {
		t.Errorf("int64 kind = %v, want Struct (a builtin)", k)
	}
}

func TestCompletionOffersDeclaredTypes(t *testing.T) {
	// A user-declared type is offered in a type position, as a Class.
	src := "pub type Level = int8\nconst x: Level = 1\n"
	doc := testView(src)
	offset := strings.Index(src, ": Level") + 3 // inside "Level" annotation
	got := byLabel(completion(doc, offset).Items)
	if _, ok := got["Level"]; !ok {
		t.Fatal("type completion missing the declared type Level")
	}
	if k := got["Level"].Kind; k == nil || *k != protocol.CompletionItemKindClass {
		t.Errorf("Level kind = %v, want Class (a declared type)", k)
	}
}

func TestCompletionDedupesNames(t *testing.T) {
	// A redeclared name contributes a single completion item.
	doc := testView("const A = 1\nconst A = 2\nconst B = A\n")
	offset := strings.Index("const A = 1\nconst A = 2\nconst B = A\n", "= A") + 3
	n := 0
	for _, it := range completion(doc, offset).Items {
		if it.Label == "A" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("got %d completion items for A, want 1", n)
	}
}

func TestCompletionInAssertCondition(t *testing.T) {
	// An assert's condition is a value position: constants and the value
	// keywords are offered, type names are not the candidates.
	src := "/// the maximum\nconst Max: int64 = 100\nassert Max > 0\n"
	doc := testView(src)

	offset := strings.Index(src, "assert Max") + len("assert Ma")
	got := byLabel(completion(doc, offset).Items)

	for _, want := range []string{"Max", "true", "false", "null", "fn"} {
		if _, ok := got[want]; !ok {
			t.Errorf("assert-condition completion missing %q", want)
		}
	}
	if d := got["Max"].Detail; d != ": int64" {
		t.Errorf("Max detail = %q, want %q", d, ": int64")
	}
}

func TestMemberCompletion(t *testing.T) {
	src := "const xs: list<int8> = [1]\nconst ys = xs.map(fn(x: int8): int8 { return x })\n"
	doc := testView(src)

	got := byLabel(completion(doc, strings.Index(src, "xs.map")+4).Items)

	// The receiver's methods, not the value namespace.
	if _, ok := got["xs"]; ok {
		t.Error("member completion offered the constant xs")
	}
	if _, ok := got["true"]; ok {
		t.Error("member completion offered a value keyword")
	}

	m, ok := got["map"]
	if !ok {
		t.Fatalf("member completion missing map; got %v", got)
	}
	if m.Kind == nil || *m.Kind != protocol.CompletionItemKindMethod {
		t.Errorf("map kind = %v, want Method", m.Kind)
	}
	if want := "pub extern map(func: fn(int8): R): list<R>"; m.Detail != want {
		t.Errorf("map detail = %q, want %q", m.Detail, want)
	}
	// The function parameter expands to a fn literal, the solved element type
	// annotated, the unsolved result left to inference.
	if want := "map(fn(${1:x}: int8) { ${2} })"; m.InsertText != want {
		t.Errorf("map snippet = %q, want %q", m.InsertText, want)
	}
	if m.InsertTextFormat == nil || *m.InsertTextFormat != protocol.InsertTextFormatSnippet {
		t.Errorf("map insert format = %v, want snippet", m.InsertTextFormat)
	}
	if m.Documentation == nil || !strings.Contains(m.Documentation.Value, "func applied to each element") {
		t.Errorf("map documentation = %v, want the prelude doc", m.Documentation)
	}

	// A plain parameter is a plain tab stop.
	if add, ok := got["add"]; !ok || add.InsertText != "add(${1:other})" {
		t.Errorf("add snippet = %+v, want add(${1:other})", got["add"])
	}
	if l, ok := got["len"]; !ok || l.InsertText != "len()" {
		t.Errorf("len snippet = %+v, want len()", got["len"])
	}
}

func TestMemberCompletionAfterBareDot(t *testing.T) {
	// The moment after typing the dot: the parse recovered a member access
	// with its name missing, and completion already knows the receiver.
	src := "const xs: list<int8> = [1]\nconst ys = xs.\n"
	doc := testView(src)

	got := byLabel(completion(doc, strings.Index(src, "xs.")+3).Items)
	for _, want := range []string{"map", "len", "add", "eql", "neq"} {
		if _, ok := got[want]; !ok {
			t.Errorf("completion after the dot missing %q; got %v", want, got)
		}
	}
}

func TestMemberCompletionFields(t *testing.T) {
	src := "type Rec = {\n  id: int8\n  level: int16\n} impl {\n  get(): int8 {\n    return self.id\n  }\n}\n"
	doc := testView(src)

	got := byLabel(completion(doc, strings.Index(src, "self.id")+6).Items)
	id, ok := got["id"]
	if !ok {
		t.Fatalf("member completion missing the field id; got %v", got)
	}
	if id.Kind == nil || *id.Kind != protocol.CompletionItemKindField || id.Detail != ": int8" {
		t.Errorf("id item = %+v, want a Field: int8", id)
	}
	if _, ok := got["level"]; !ok {
		t.Error("member completion missing the field level")
	}
	if _, ok := got["get"]; !ok {
		t.Error("member completion missing the method get")
	}
}
