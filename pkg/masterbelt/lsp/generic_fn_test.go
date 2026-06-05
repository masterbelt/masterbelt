package lsp

import (
	"strings"
	"testing"
)

// genericFnLSPSrc is an interface, an opt-in implementor, and two generic
// functions — a bounded one whose parameter calls the bound interface, and an
// unbounded pass-through.
const genericFnLSPSrc = "" +
	"pub interface foldable<K, V> {\n" +
	"  fold<A>(init: A, step: fn(acc: A, key: K, value: V): A): A\n" +
	"}\n" +
	"pub type Bag = list<int> impl foldable<int, int> {\n" +
	"  fold<A>(init: A, step: fn(acc: A, key: int, value: int): A): A {\n" +
	"    return init\n" +
	"  }\n" +
	"}\n" +
	"/// total of a foldable collection\n" +
	"pub fn total<T: foldable<int, int>>(c: T): int {\n" +
	"  return c.fold(0, fn(acc, key, value) -> acc + value)\n" +
	"}\n" +
	"pub fn identity<T>(x: T): T {\n" +
	"  return x\n" +
	"}\n"

// TestHoverGenericFunctionSignature checks a generic function's hover card shows
// its type-parameter list with the bound.
func TestHoverGenericFunctionSignature(t *testing.T) {
	doc := testView(genericFnLSPSrc)

	t.Run("bounded function", func(t *testing.T) {
		h := hover(doc, strings.Index(genericFnLSPSrc, "total<"))
		if h == nil {
			t.Fatal("no hover on the declared function name")
		}
		if !strings.Contains(h.Contents.Value, "pub fn total<T: foldable<int, int>>(c: T): int") {
			t.Errorf("hover = %q, want the generic signature with the bound", h.Contents.Value)
		}
	})

	t.Run("unbounded function", func(t *testing.T) {
		h := hover(doc, strings.Index(genericFnLSPSrc, "identity<"))
		if h == nil {
			t.Fatal("no hover on the declared function name")
		}
		if !strings.Contains(h.Contents.Value, "pub fn identity<T>(x: T): T") {
			t.Errorf("hover = %q, want the unbounded generic signature", h.Contents.Value)
		}
	})
}

// TestCompletionBoundedParamMethods checks member completion on a bounded type
// parameter offers the bound interface's methods (fold) — the methods the
// parameter is fixed to.
func TestCompletionBoundedParamMethods(t *testing.T) {
	doc := testView(genericFnLSPSrc)

	// The cursor on the member access c.fold inside total's body.
	offset := strings.Index(genericFnLSPSrc, "c.fold") + len("c.")
	got := byLabel(completion(doc, offset).Items)

	if _, ok := got["fold"]; !ok {
		t.Fatalf("member completion on c: T (T: foldable<int, int>) missing fold; got %v", got)
	}
}
