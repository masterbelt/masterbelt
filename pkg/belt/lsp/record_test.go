package lsp

import (
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
)

// recordSrc declares a record type and both literal forms. The typed literal
// initializes x only (y is the candidate the completion should offer); the
// inferred literal is typed by its annotation.
const recordSrc = "pub type Point = { x: nint, y: nint }\n" +
	"const O = Point{ x: 1 }\n" +
	"const P: Point = { x: 2, y: 3 }\n"

func TestCompletionRecordFieldsTypedForm(t *testing.T) {
	doc := testView(recordSrc)

	// Just inside the typed literal's braces, after "x: 1," would be ideal;
	// probe right before the closing brace of Point{ x: 1 }.
	offset := strings.Index(recordSrc, "x: 1 }") + len("x: 1 ")
	got := byLabel(completion(doc, offset).Items)

	if _, ok := got["y"]; !ok {
		t.Fatalf("field completion missing y: %v", got)
	}
	if d := got["y"].Detail; d != ": nint" {
		t.Errorf("y detail = %q, want %q", d, ": nint")
	}
	if k := got["y"].Kind; k == nil || *k != protocol.CompletionItemKindField {
		t.Errorf("y kind = %v, want Field", k)
	}
	// x is already initialized; the value namespace is not offered either.
	if _, ok := got["x"]; ok {
		t.Error("field completion offered the already-initialized x")
	}
	if _, ok := got["O"]; ok {
		t.Error("field completion offered the constant O")
	}
}

func TestCompletionRecordFieldsInferredForm(t *testing.T) {
	doc := testView("pub type Point = { x: nint, y: nint }\nconst P: Point = { }\n")

	offset := strings.Index("pub type Point = { x: nint, y: nint }\nconst P: Point = { }\n", "= { }") + 4
	got := byLabel(completion(doc, offset).Items)

	for _, want := range []string{"x", "y"} {
		if _, ok := got[want]; !ok {
			t.Errorf("field completion missing %q (from the annotation's expected type)", want)
		}
	}
}

func TestCompletionRecordFieldPartialName(t *testing.T) {
	// The partial field name being typed is itself offered (not counted as
	// already written).
	src := "pub type Point = { x: nint, yy: nint }\nconst O = Point{ x: 1, y }\n"
	doc := testView(src)
	offset := strings.Index(src, ", y }") + len(", y")
	got := byLabel(completion(doc, offset).Items)
	if _, ok := got["yy"]; !ok {
		t.Errorf("field completion missing yy while typing its prefix: %v", got)
	}
}

func TestCompletionRecordFieldValueIsValuePosition(t *testing.T) {
	// Past the colon the field's value is a plain value position: constants
	// are offered, field names are not.
	src := "pub type Point = { x: nint, y: nint }\nconst Max = 9\nconst O = Point{ x: Max, y: 1 }\n"
	doc := testView(src)
	offset := strings.Index(src, "x: Max") + len("x: Ma")
	got := byLabel(completion(doc, offset).Items)
	if _, ok := got["Max"]; !ok {
		t.Errorf("value completion missing Max in a field value: %v", got)
	}
	if _, ok := got["y"]; ok {
		t.Error("field name y offered inside a field value")
	}
}

func TestCompletionRecordFieldsNested(t *testing.T) {
	// The expected type reaches a nested inferred literal through the outer
	// literal's field type.
	src := "pub type Point = { x: nint, y: nint }\n" +
		"pub type Item = { id: nint, pos: Point }\n" +
		"const Sword = Item{ id: 1, pos: { } }\n"
	doc := testView(src)
	offset := strings.Index(src, "pos: { }") + len("pos: { ")
	got := byLabel(completion(doc, offset).Items)
	for _, want := range []string{"x", "y"} {
		if _, ok := got[want]; !ok {
			t.Errorf("nested field completion missing %q: %v", want, got)
		}
	}
	if _, ok := got["id"]; ok {
		t.Error("nested field completion offered the outer record's id")
	}
}

func TestHoverRecordField(t *testing.T) {
	doc := testView(recordSrc)

	t.Run("typed form field name", func(t *testing.T) {
		offset := strings.Index(recordSrc, "x: 1")
		h := hover(doc, offset)
		if h == nil {
			t.Fatal("no hover on the field initializer's name")
		}
		if !strings.Contains(h.Contents.Value, "x: nint") {
			t.Errorf("hover = %q, want x: nint", h.Contents.Value)
		}
	})

	t.Run("inferred form field name", func(t *testing.T) {
		offset := strings.Index(recordSrc, "y: 3")
		h := hover(doc, offset)
		if h == nil {
			t.Fatal("no hover on the inferred literal's field name")
		}
		if !strings.Contains(h.Contents.Value, "y: nint") {
			t.Errorf("hover = %q, want y: nint", h.Contents.Value)
		}
	})

	t.Run("typed form type name", func(t *testing.T) {
		offset := strings.Index(recordSrc, "Point{")
		h := hover(doc, offset)
		if h == nil {
			t.Fatal("no hover on the literal's type name")
		}
		if !strings.Contains(h.Contents.Value, "type Point = { x: nint, y: nint }") {
			t.Errorf("hover = %q, want the Point type card", h.Contents.Value)
		}
	})
}

func TestSemanticTokensRecordLiteral(t *testing.T) {
	// The typed form's name colours as a type, the field initializers' names
	// as properties — matching the record type's declared fields.
	doc := abstract.NewDocument([]byte("const O = Point{ x: 1 }\n"))
	got := decode(semanticTokens(doc).Data)

	want := []decodedToken{
		{0, 0, 5, stKeyword, 0},                           // const
		{0, 6, 1, stVariable, smDeclaration | smReadonly}, // O
		{0, 8, 1, stOperator, 0},                          // =
		{0, 10, 5, stType, 0},                             // Point
		{0, 17, 1, stProperty, 0},                         // x
		{0, 18, 1, stOperator, 0},                         // :
		{0, 20, 1, stNumber, 0},                           // 1
	}

	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d:\n got:  %+v\n want: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
