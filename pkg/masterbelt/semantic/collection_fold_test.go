package semantic

import "testing"

// These tests pin the collection folding completeness work (stream A): the
// native list.len and the native fold (list and map), and the foldable provided
// methods defined on top of fold — count, any, all, map, filter, keys, values —
// which all fold at compile time through the real prelude (collection.belt).

// TestListLenFolds checks that list.len() folds to the element count — the
// intrinsic E-18 left for map but not list.
func TestListLenFolds(t *testing.T) {
	src := "const N = [1, 2, 3].len()\n"
	if got := evalOf(t, src, "N").Int.Int64(); got != 3 {
		t.Errorf("[1,2,3].len() = %d, want 3", got)
	}
}

// TestMapLenFolds re-pins the map.len() fold for parity with the list one.
func TestMapLenFolds(t *testing.T) {
	src := "const N = [\"a\": 1, \"b\": 2].len()\n"
	if got := evalOf(t, src, "N").Int.Int64(); got != 2 {
		t.Errorf("map.len() = %d, want 2", got)
	}
}

// TestListFoldFolds checks the native list fold: the step sees (acc, index,
// value), threaded from the init.
func TestListFoldFolds(t *testing.T) {
	src := "const S = [1, 2, 3].fold(0, fn(a: int, k: int, v: int): int -> a + v)\n"
	if got := evalOf(t, src, "S").Int.Int64(); got != 6 {
		t.Errorf("sum fold = %d, want 6", got)
	}
}

// TestListFoldUsesIndex checks that the list fold's key is the element index, in
// order: summing the indices of a 3-element list is 0+1+2 = 3.
func TestListFoldUsesIndex(t *testing.T) {
	src := "const S = [10, 20, 30].fold(0, fn(a: int, k: int, v: int): int -> a + k)\n"
	if got := evalOf(t, src, "S").Int.Int64(); got != 3 {
		t.Errorf("index fold = %d, want 3 (0+1+2)", got)
	}
}

// TestMapFoldFolds checks the native map fold: the step sees (acc, key, value).
func TestMapFoldFolds(t *testing.T) {
	src := "const S = [\"a\": 1, \"b\": 2, \"c\": 3].fold(0, fn(a: int, k: string, v: int): int -> a + v)\n"
	if got := evalOf(t, src, "S").Int.Int64(); got != 6 {
		t.Errorf("map value fold = %d, want 6", got)
	}
}

// TestListCountFolds checks the provided count method folds through fold.
func TestListCountFolds(t *testing.T) {
	src := "const N = [1, 2, 3].count()\n"
	if got := evalOf(t, src, "N").Int.Int64(); got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
}

// TestMapCountFolds checks count on a map folds too.
func TestMapCountFolds(t *testing.T) {
	src := "const N = [\"a\": 1, \"b\": 2].count()\n"
	if got := evalOf(t, src, "N").Int.Int64(); got != 2 {
		t.Errorf("map count = %d, want 2", got)
	}
}

// TestListMapProvidedFolds checks the foldable map (distinct from list's own
// inherent map): map(fn(v) -> v * 2) over a list folds to the doubled list.
func TestListMapProvidedFolds(t *testing.T) {
	src := "const Xs = [1, 2, 3].map(fn(v: int): int -> v * 2)\n"
	v := evalOf(t, src, "Xs")
	if got := v.String(); got != "[2, 4, 6]" {
		t.Errorf("map = %s, want [2, 4, 6]", got)
	}
}

// TestListFilterFolds checks filter keeps the values satisfying the predicate.
func TestListFilterFolds(t *testing.T) {
	src := "const Xs = [1, 2, 3, 4].filter(fn(v: int): bool -> v > 2)\n"
	v := evalOf(t, src, "Xs")
	if got := v.String(); got != "[3, 4]" {
		t.Errorf("filter = %s, want [3, 4]", got)
	}
}

// TestListAnyAllFold checks any and all fold to bools.
func TestListAnyAllFold(t *testing.T) {
	any := "const B = [1, 2, 3].any(fn(v: int): bool -> v > 2)\n"
	if !evalOf(t, any, "B").Bool {
		t.Errorf("any(>2) = false, want true")
	}
	all := "const B = [1, 2, 3].all(fn(v: int): bool -> v > 2)\n"
	if evalOf(t, all, "B").Bool {
		t.Errorf("all(>2) = true, want false")
	}
	allTrue := "const B = [1, 2, 3].all(fn(v: int): bool -> v > 0)\n"
	if !evalOf(t, allTrue, "B").Bool {
		t.Errorf("all(>0) = false, want true")
	}
}

// TestMapKeysValuesFold checks keys and values fold to lists in fold order.
func TestMapKeysValuesFold(t *testing.T) {
	keys := "const Ks = [\"a\": 10, \"b\": 20].keys()\n"
	if got := evalOf(t, keys, "Ks").String(); got != `["a", "b"]` {
		t.Errorf("keys = %s, want [\"a\", \"b\"]", got)
	}
	values := "const Vs = [\"a\": 10, \"b\": 20].values()\n"
	if got := evalOf(t, values, "Vs").String(); got != "[10, 20]" {
		t.Errorf("values = %s, want [10, 20]", got)
	}
}

// TestListValuesFold checks values on a list yields the elements in order.
func TestListValuesFold(t *testing.T) {
	src := "const Vs = [7, 8, 9].values()\n"
	if got := evalOf(t, src, "Vs").String(); got != "[7, 8, 9]" {
		t.Errorf("list values = %s, want [7, 8, 9]", got)
	}
}
