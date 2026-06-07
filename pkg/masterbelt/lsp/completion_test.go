package lsp

import (
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
)

// completionSrc has a documented, annotated constant and a second constant whose
// initializer is a value position; "int64" is the only type position.
const completionSrc = "/// the maximum\nconst Max: long = 100\nconst Cur = Max\n"

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
	if d := got["Max"].Detail; d != ": long" {
		t.Errorf("Max detail = %q, want %q", d, ": long")
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
	offset := strings.Index(completionSrc, "long") + 2
	got := byLabel(completion(doc, offset).Items)

	for _, want := range []string{"long", "bool", "string"} {
		if _, ok := got[want]; !ok {
			t.Errorf("type completion missing builtin %q", want)
		}
	}
	if _, ok := got["Max"]; ok {
		t.Error("type completion offered the constant Max")
	}
	if k := got["long"].Kind; k == nil || *k != protocol.CompletionItemKindStruct {
		t.Errorf("long kind = %v, want Struct (a builtin)", k)
	}
}

func TestCompletionOffersDeclaredTypes(t *testing.T) {
	// A user-declared type is offered in a type position, as a Class.
	src := "pub type Level = sbyte\nconst x: Level = 1\n"
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

func TestCompletionInEnumBaseType(t *testing.T) {
	// An enum's base-type position offers only the legal base types — the
	// integer family and string — matching the semantic analyzer's set, not the
	// general type completion (which would offer bool, datetime, and user types).
	src := "type Level = sbyte\nenum Rarity: sbyte {\n  Common\n}\n"
	doc := testView(src)

	offset := strings.Index(src, "Rarity: sbyte") + len("Rarity: in")
	got := byLabel(completion(doc, offset).Items)

	for _, want := range []string{"nint", "sbyte", "long", "nuint", "ulong", "string"} {
		if _, ok := got[want]; !ok {
			t.Errorf("enum base completion missing %q", want)
		}
	}
	for _, unwanted := range []string{"bool", "datetime", "duration", "null", "error", "Level"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("enum base completion offered non-base type %q", unwanted)
		}
	}
}

func TestCompletionInAssertCondition(t *testing.T) {
	// An assert's condition is a value position: constants and the value
	// keywords are offered, type names are not the candidates.
	src := "/// the maximum\nconst Max: long = 100\nassert Max > 0\n"
	doc := testView(src)

	offset := strings.Index(src, "assert Max") + len("assert Ma")
	got := byLabel(completion(doc, offset).Items)

	for _, want := range []string{"Max", "true", "false", "null", "fn"} {
		if _, ok := got[want]; !ok {
			t.Errorf("assert-condition completion missing %q", want)
		}
	}
	if d := got["Max"].Detail; d != ": long" {
		t.Errorf("Max detail = %q, want %q", d, ": long")
	}
}

func TestCompletionInWhereClause(t *testing.T) {
	// A where-clause predicate is a value position: the value keywords are
	// offered, not the type names the declaration's body position would be.
	src := "type Port = int where self >= 1\n"
	doc := testView(src)

	offset := strings.Index(src, "self >=") + len("se")
	got := byLabel(completion(doc, offset).Items)

	for _, want := range []string{"true", "false", "null", "fn"} {
		if _, ok := got[want]; !ok {
			t.Errorf("where-clause completion missing %q", want)
		}
	}
	if _, ok := got["int"]; ok {
		t.Error("where-clause completion offered a type name")
	}
}

func TestMemberCompletion(t *testing.T) {
	src := "const xs: list<sbyte> = [1]\nconst ys = xs.map(fn(x: sbyte): sbyte { return x })\n"
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
	if want := "pub extern map(func: fn(sbyte): R): list<R>"; m.Detail != want {
		t.Errorf("map detail = %q, want %q", m.Detail, want)
	}
	// The function parameter expands to an arrow-bodied fn literal, the solved
	// element type annotated, the unsolved result left to inference.
	if want := "map(fn(${1:x}: sbyte) -> ${2})"; m.InsertText != want {
		t.Errorf("map snippet = %q, want %q", m.InsertText, want)
	}
	if m.InsertTextFormat == nil || *m.InsertTextFormat != protocol.InsertTextFormatSnippet {
		t.Errorf("map insert format = %v, want snippet", m.InsertTextFormat)
	}
	if m.Documentation == nil || !strings.Contains(m.Documentation.Value, "func applied to each element") {
		t.Errorf("map documentation = %v, want the prelude doc", m.Documentation)
	}

	// A plain parameter is a plain tab stop.
	if push, ok := got["push"]; !ok || push.InsertText != "push(${1:value})" {
		t.Errorf("push snippet = %+v, want push(${1:value})", got["push"])
	}
	if l, ok := got["len"]; !ok || l.InsertText != "len()" {
		t.Errorf("len snippet = %+v, want len()", got["len"])
	}
}

func TestMemberCompletionOverloads(t *testing.T) {
	// An overloaded name completes once per signature, so the editor's list
	// shows what each call shape would mean.
	src := "pub type Score = int impl {\n" +
		"  pub fn merge(points: self): self {\n    return self + points\n  }\n" +
		"  pub fn merge(active: bool): bool {\n    return active && self > 0\n  }\n" +
		"}\n" +
		"const Base: Score = 100\n" +
		"const Bumped = Base.merge(50)\n"
	doc := testView(src)

	items := completion(doc, strings.Index(src, "Base.merge(50)")+7).Items
	var details []string
	for _, item := range items {
		if item.Label == "merge" {
			details = append(details, item.Detail)
		}
	}
	if len(details) != 2 {
		t.Fatalf("merge completes %d times, want 2: %v", len(details), details)
	}
	want := map[string]bool{
		"pub merge(points: self): self": true,
		"pub merge(active: bool): bool": true,
	}
	for _, d := range details {
		if !want[d] {
			t.Errorf("unexpected merge signature %q", d)
		}
		delete(want, d)
	}
}

func TestMemberCompletionAfterBareDot(t *testing.T) {
	// The moment after typing the dot: the parse recovered a member access
	// with its name missing, and completion already knows the receiver.
	src := "const xs: list<sbyte> = [1]\nconst ys = xs.\n"
	doc := testView(src)

	got := byLabel(completion(doc, strings.Index(src, "xs.")+3).Items)
	for _, want := range []string{"map", "len", "add", "eql", "neq"} {
		if _, ok := got[want]; !ok {
			t.Errorf("completion after the dot missing %q; got %v", want, got)
		}
	}
}

func TestMemberCompletionFields(t *testing.T) {
	src := "type Rec = {\n  id: sbyte\n  level: short\n} impl {\n  get(): sbyte {\n    return self.id\n  }\n}\n"
	doc := testView(src)

	got := byLabel(completion(doc, strings.Index(src, "self.id")+6).Items)
	id, ok := got["id"]
	if !ok {
		t.Fatalf("member completion missing the field id; got %v", got)
	}
	if id.Kind == nil || *id.Kind != protocol.CompletionItemKindField || id.Detail != ": sbyte" {
		t.Errorf("id item = %+v, want a Field: sbyte", id)
	}
	if _, ok := got["level"]; !ok {
		t.Error("member completion missing the field level")
	}
	if _, ok := got["get"]; !ok {
		t.Error("member completion missing the method get")
	}
}

func TestCompletionOffersErrorConstructor(t *testing.T) {
	doc := testView(completionSrc)

	// A value position offers the error constructor as a fill-in snippet.
	offset := strings.Index(completionSrc, "= Max") + 3
	got := byLabel(completion(doc, offset).Items)
	item, ok := got["error"]
	if !ok {
		t.Fatalf("value completion missing the error constructor")
	}
	if item.Kind == nil || *item.Kind != protocol.CompletionItemKindConstructor {
		t.Errorf("error kind = %v, want Constructor", item.Kind)
	}
	if item.InsertText != "error(\"${1:message}\")" {
		t.Errorf("error insert = %q, want the call snippet", item.InsertText)
	}
	if item.Detail != "error(message: string)" {
		t.Errorf("error detail = %q, want the constructor signature", item.Detail)
	}

	// A type position offers the error type itself.
	typeOffset := strings.Index(completionSrc, "long") + 2
	types := byLabel(completion(doc, typeOffset).Items)
	if _, ok := types["error"]; !ok {
		t.Errorf("type completion missing error")
	}
}

// TestCompletionOffersRangeConstructor checks that the range constructor is
// offered in a value position with its own two-argument snippet and signature —
// the same constructor surface error has, but for range(start, end) — and that
// the range type itself is offered in a type position.
func TestCompletionOffersRangeConstructor(t *testing.T) {
	doc := testView(completionSrc)

	offset := strings.Index(completionSrc, "= Max") + 3
	got := byLabel(completion(doc, offset).Items)
	item, ok := got["range"]
	if !ok {
		t.Fatalf("value completion missing the range constructor")
	}
	if item.Kind == nil || *item.Kind != protocol.CompletionItemKindConstructor {
		t.Errorf("range kind = %v, want Constructor", item.Kind)
	}
	if item.InsertText != "range(${1:start}, ${2:end})" {
		t.Errorf("range insert = %q, want the two-argument call snippet", item.InsertText)
	}
	if item.Detail != "range(start: nint, end: nint)" {
		t.Errorf("range detail = %q, want the constructor signature", item.Detail)
	}
	if item.Documentation == nil || !strings.Contains(item.Documentation.Value, "A sequence of integers") {
		t.Errorf("range documentation = %v, want the doc comment", item.Documentation)
	}

	// A type position offers the range type itself.
	typeOffset := strings.Index(completionSrc, "long") + 2
	types := byLabel(completion(doc, typeOffset).Items)
	if _, ok := types["range"]; !ok {
		t.Errorf("type completion missing range")
	}
}

func TestMemberCompletionOnError(t *testing.T) {
	src := "const E = error(\"boom\")\nconst M = E.message()\n"
	doc := testView(src)

	offset := strings.Index(src, "E.") + 2
	items, ok := memberItems(doc, offset)
	if !ok {
		t.Fatal("not recognized as a member position")
	}
	got := byLabel(items)
	if _, ok := got["message"]; !ok {
		t.Errorf("member completion on an error value missing message, got %v", items)
	}
}

func TestCompletionEffectfulContext(t *testing.T) {
	src := "extern fn io async fetch(url: string): string\n" +
		"fn pure(): nint -> 1\n" +
		"pub fn io async page(url: string): string {\n" +
		"  return await fetch(url)\n" +
		"}\n" +
		"const A = pure()\n"
	doc := testView(src)

	// Inside a function body, effectful functions and await are offered.
	offset := strings.Index(src, "await fetch") + 2
	got := byLabel(completion(doc, offset).Items)
	for _, want := range []string{"fetch", "pure", "await"} {
		if _, ok := got[want]; !ok {
			t.Errorf("body completion missing %q", want)
		}
	}
	if d := got["fetch"].Detail; !strings.Contains(d, "extern fn io async fetch") {
		t.Errorf("fetch detail = %q, want the effectful signature", d)
	}

	// A constant initializer is a pure position: the effectful function and
	// await are suppressed, the pure one stays.
	offset = strings.Index(src, "pure()\n") + 2
	got = byLabel(completion(doc, offset).Items)
	if _, ok := got["fetch"]; ok {
		t.Errorf("pure position offers the effectful fetch")
	}
	if _, ok := got["await"]; ok {
		t.Errorf("pure position offers await")
	}
	if _, ok := got["pure"]; !ok {
		t.Errorf("pure position missing the pure function")
	}
}
