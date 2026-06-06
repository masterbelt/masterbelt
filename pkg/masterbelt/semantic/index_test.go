package semantic

import "testing"

// A read coll[i] desugars to coll.get(i): a fallible lookup whose result is a
// union (V | error). A write coll[i] = v desugars to coll = coll.set(i, v): a
// rebind of the collection. The reads and the in-range writes analyze cleanly;
// the only new diagnostic is a list write past the end (index_out_of_range),
// which the const/immutable-data rules of E-15 still pre-empt for an immutable
// target.

// TestIndexReadOK checks that index reads — a list element, a map value, a
// dynamic index, a chained subscript — analyze without a diagnostic: the get
// method resolves and its union result is well-typed.
func TestIndexReadOK(t *testing.T) {
	cases := []string{
		"const Xs = [10, 20, 30]\nconst A: int | error = Xs[0]\n",
		"const Tbl = [\"k\": 1]\nconst B: int | error = Tbl[\"k\"]\n",
		"pub fn at(xs: list<int>, i: int): int | error {\n  return xs[i]\n}\n",
	}
	for _, src := range cases {
		_, diags := analyze(src)
		if len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics: %v", src, codes(diags))
		}
	}
}

// TestChainedIndexNeedsUnwrap pins the MVP limitation that a chained subscript
// m[0][1] does not type-check on its own: the first read yields list<int> |
// error, and a method (the second get) on that union does not resolve — the
// intermediate error must be handled first. This is the result-union cost the
// plan accepts until in-range narrowing lands.
func TestChainedIndexNeedsUnwrap(t *testing.T) {
	_, diags := analyze("pub fn nested(m: list<list<int>>): int | error {\n  return m[0][1]\n}\n")
	if !hasCode(diags, CodeInvalidOperation) {
		t.Errorf("want invalid_operation for a subscript on a union, got %v", codes(diags))
	}
}

// TestIndexWriteOK checks that in-range list writes and map upserts on a let
// local analyze cleanly: the rebind stays a let-local assignment, and a map set
// is never out of range.
func TestIndexWriteOK(t *testing.T) {
	cases := []string{
		"pub fn f(): list<int> {\n  let xs = [1, 2, 3]\n  xs[0] = 9\n  return xs\n}\n",
		"pub fn f(): list<int> {\n  let xs = [1, 2, 3]\n  xs[2] = 9\n  return xs\n}\n",
		"pub fn f(): map<string, int> {\n  let m = [\"a\": 1]\n  m[\"a\"] = 9\n  m[\"b\"] = 2\n  return m\n}\n",
	}
	for _, src := range cases {
		_, diags := analyze(src)
		if len(diags) != 0 {
			t.Errorf("%q: unexpected diagnostics: %v", src, codes(diags))
		}
	}
}

// TestIndexOutOfRange checks the compile-time out-of-range list write: an index
// past the end, and a negative one, are reported on a let-bound list whose length
// is statically known. A map write is an upsert and is never out of range.
func TestIndexOutOfRange(t *testing.T) {
	bad := []string{
		"pub fn f(): list<int> {\n  let xs = [1, 2, 3]\n  xs[9] = 0\n  return xs\n}\n",
		"pub fn f(): list<int> {\n  let xs = [1, 2, 3]\n  xs[3] = 0\n  return xs\n}\n", // one past the end
		"pub fn f(): list<int> {\n  let xs = [1, 2, 3]\n  xs[-1] = 0\n  return xs\n}\n",
		"pub fn f(): list<int> {\n  let xs: list<int> = []\n  xs[0] = 0\n  return xs\n}\n", // empty list
	}
	for _, src := range bad {
		_, diags := analyze(src)
		if !hasCode(diags, CodeIndexOutOfRange) {
			t.Errorf("%q: want index_out_of_range, got %v", src, codes(diags))
		}
	}

	ok := []string{
		"pub fn f(): map<string, int> {\n  let m = [\"a\": 1]\n  m[\"z\"] = 0\n  return m\n}\n",
		"pub fn f(): list<int> {\n  let xs = [1, 2, 3]\n  xs[2] = 0\n  return xs\n}\n",
	}
	for _, src := range ok {
		_, diags := analyze(src)
		if hasCode(diags, CodeIndexOutOfRange) {
			t.Errorf("%q: unexpected index_out_of_range: %v", src, codes(diags))
		}
	}
}

// TestIndexWriteToConst checks that a write to a const collection is rejected as
// assign_to_const (a const is immutable), the E-15 rule, rather than as an
// out-of-range write: the immutable target is caught before the bounds.
func TestIndexWriteToConst(t *testing.T) {
	_, diags := analyze("const Ys = [1, 2, 3]\npub fn f(): int {\n  Ys[0] = 9\n  return 0\n}\n")
	if !hasCode(diags, CodeAssignToConst) {
		t.Errorf("want assign_to_const, got %v", codes(diags))
	}
}

// TestIndexWriteDynamic checks that a write whose index or receiver is not
// statically foldable — a parameter index — is not reported: an unknowable index
// is the runtime's concern, not a compile-time error.
func TestIndexWriteDynamic(t *testing.T) {
	_, diags := analyze("pub fn f(i: int): list<int> {\n  let xs = [1, 2, 3]\n  xs[i] = 0\n  return xs\n}\n")
	if hasCode(diags, CodeIndexOutOfRange) {
		t.Errorf("a dynamic index must not be reported: %v", codes(diags))
	}
}

// TestCompositeMapKeyFold checks that a map keyed by a composite value — a list
// or a record — folds its get and set correctly: get finds the structurally
// equal key, and set upserts it rather than appending a duplicate entry. Before
// constEqual learned the composite kinds, a list/record key always compared
// unequal, so the read mis-folded to a key-not-found error and the write built a
// map with two entries for the same key.
// TestEmptyMapConstUpsertFolds is the E-18 follow-up's main case: an empty
// collection typed as a map (const M: map<string, int> = []) carries the map
// mapness through its annotation, so a set on it upserts and folds — where the old
// "empty reads as list" default left the upsert unfolded. The empty list stays a
// list (its set at index 0 does not fold, an out-of-range write). The empty map
// renders [:], the empty list [].
func TestEmptyMapConstUpsertFolds(t *testing.T) {
	src := "const EM: map<string, int> = []\n" +
		"const EM1 = EM.set(\"a\", 1)\n" +
		"const EM2 = EM.set(\"a\", 1).set(\"b\", 2)\n" +
		"const EL: list<int> = []\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	want := map[string]string{
		"EM":  "[:]",
		"EM1": `["a": 1]`,
		"EM2": `["a": 1, "b": 2]`,
		"EL":  "[]",
	}
	for _, c := range m.Consts {
		if w, ok := want[c.Name]; ok {
			if c.Eval == nil || c.Eval.String() != w {
				t.Errorf("%s = %v, want %s", c.Name, c.Eval, w)
			}
		}
	}
}

// TestEmptyMapLetUpsertNoOOB checks the let-annotation channel on the write check:
// an empty map let (let m: map<int, int> = []) is a settled map, so an int-keyed
// write is an upsert and must not be reported as index_out_of_range — the
// false positive the old entry-scanning isMapConst would have produced for an
// int-keyed empty map.
func TestEmptyMapLetUpsertNoOOB(t *testing.T) {
	src := "pub fn f(): map<int, int> {\n  let m: map<int, int> = []\n  m[0] = 1\n  return m\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeIndexOutOfRange) {
		t.Errorf("an empty-map upsert must not be reported out of range: %v", codes(diags))
	}
}

func TestCompositeMapKeyFold(t *testing.T) {
	src := "const M: map<list<int>, string> = [[1, 2]: \"a\"]\n" +
		"const V = M[[1, 2]]\n" +
		"const M2 = M.set([1, 2], \"b\")\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	for _, c := range m.Consts {
		switch c.Name {
		case "V":
			if c.Eval == nil || c.Eval.Str != "a" {
				t.Errorf("V = %v, want the present key's value \"a\"", c.Eval)
			}
		case "M2":
			if c.Eval == nil || c.Eval.String() != `[[1, 2]: "b"]` {
				t.Errorf("M2 = %v, want a single upserted entry [[1, 2]: \"b\"]", c.Eval)
			}
		}
	}
}
