package ir

import (
	"math"
	"math/big"
	"testing"
)

// TestConstantString pins the canonical rendering of the millisecond-backed
// constants: UTC instants, largest-units-first durations, and the signed
// extremes — including the most negative int64, whose magnitude has no int64
// negation.
func TestConstantString(t *testing.T) {
	cases := []struct {
		c    *Constant
		want string
	}{
		{DatetimeConstant(0), "D1970-01-01T00:00:00.000Z"},
		{DatetimeConstant(-1000), "D1969-12-31T23:59:59.000Z"},
		{DurationConstant(0), "0ms"},
		{DurationConstant(90 * 60 * 1000), "1h30m"},
		{DurationConstant(-90 * 60 * 1000), "-1h30m"},
		{DurationConstant(3*604_800_000 + 4*86_400_000 + 5*3_600_000 + 6*60_000 + 7*1000 + 8), "3w4d5h6m7s8ms"},
		{DurationConstant(math.MaxInt64), "15250284452w3d7h12m55s807ms"},
		{DurationConstant(math.MinInt64), "-15250284452w3d7h12m55s808ms"},
	}
	for _, tc := range cases {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

// TestEnumConstant pins the enum value form: its rendering (Type.Member) and
// the name/value accessors over the member table.
func TestEnumConstant(t *testing.T) {
	def := &TypeDef{
		Name: "Rarity",
		Enum: &EnumDef{
			Base: "byte",
			Members: []EnumMember{
				{Name: "Common", Value: IntConstant(big.NewInt(1))},
				{Name: "Legend", Value: IntConstant(big.NewInt(10))},
			},
		},
	}
	legend := EnumConstant(def, 1)
	if got := legend.String(); got != "Rarity.Legend" {
		t.Errorf("String() = %q, want Rarity.Legend", got)
	}
	if got := legend.EnumName(); got != "Legend" {
		t.Errorf("EnumName() = %q, want Legend", got)
	}
	if v := legend.EnumValue(); v == nil || v.String() != "10" {
		t.Errorf("EnumValue() = %v, want 10", v)
	}
	// An out-of-range index has no name or value, rather than panicking.
	bad := EnumConstant(def, 9)
	if bad.EnumName() != "" || bad.EnumValue() != nil {
		t.Errorf("out-of-range member: want empty name and nil value, got %q / %v", bad.EnumName(), bad.EnumValue())
	}
}

// TestCollectionMapness pins the three-valued mapness of a folded collection:
// a non-empty literal settles it from its entries, an empty literal is
// CollUnknown unless built with an explicit kind, equality includes the mapness
// (an empty list, map, and unknown are pairwise unequal), and String renders an
// empty map as the [:] marker while an empty list and unknown render [].
func TestCollectionMapness(t *testing.T) {
	one := IntConstant(big.NewInt(1))
	key := StringConstant("a")

	// A non-empty literal settles its mapness by its entries.
	list := CollectionConstant([]ConstEntry{{Value: one}})
	if !list.IsList() || list.IsMap() {
		t.Errorf("non-empty bare collection: want list, got mapness %d", list.CollMapness)
	}
	m := CollectionConstant([]ConstEntry{{Key: key, Value: one}})
	if !m.IsMap() || m.IsList() {
		t.Errorf("non-empty keyed collection: want map, got mapness %d", m.CollMapness)
	}

	// An empty literal with no channel is CollUnknown — neither list nor map.
	empty := CollectionConstant(nil)
	if empty.CollMapness != CollUnknown || empty.IsList() || empty.IsMap() {
		t.Errorf("empty bare collection: want CollUnknown, got mapness %d", empty.CollMapness)
	}
	// An explicit kind settles an empty literal a channel decided.
	emptyMap := CollectionConstantOf(nil, CollMap)
	emptyList := CollectionConstantOf(nil, CollList)
	if !emptyMap.IsMap() || !emptyList.IsList() {
		t.Errorf("empty typed collections: want map/list, got %d/%d", emptyMap.CollMapness, emptyList.CollMapness)
	}

	// Equality includes mapness: the three empty kinds are pairwise unequal, and
	// same-kind empties are equal.
	if ConstantsEqual(emptyMap, emptyList) {
		t.Error("an empty map must not equal an empty list")
	}
	if ConstantsEqual(emptyMap, empty) || ConstantsEqual(emptyList, empty) {
		t.Error("a settled empty collection must not equal an unknown one")
	}
	if !ConstantsEqual(empty, CollectionConstant(nil)) {
		t.Error("two unknown empty collections should be equal")
	}
	if !ConstantsEqual(emptyMap, CollectionConstantOf(nil, CollMap)) {
		t.Error("two empty maps should be equal")
	}

	// String renders an empty map distinctly; an empty list and unknown both [].
	if got := emptyMap.String(); got != "[:]" {
		t.Errorf("empty map String() = %q, want [:]", got)
	}
	if got := emptyList.String(); got != "[]" {
		t.Errorf("empty list String() = %q, want []", got)
	}
	if got := empty.String(); got != "[]" {
		t.Errorf("empty unknown String() = %q, want []", got)
	}
}

// TestUnionTagEquality pins the tagged-union identity rule of ConstantsEqual: the
// member tag is part of a value's identity, so two values with the same payload
// but different (or one-sided) tags are unequal, and same-tag (or both-untagged)
// values fall through to their payload equality. Tagged/Untagged are pure copies
// that never mutate the original, and re-tagging with the same member is the
// identity.
func TestUnionTagEquality(t *testing.T) {
	coin := &TypeDef{Name: "Coin", Body: &Record{Fields: []Field{{Name: "amount", Type: &Builtin{Name: "nint"}}}}}
	level := &TypeDef{Name: "Level", Body: &Record{Fields: []Field{{Name: "rank", Type: &Builtin{Name: "nint"}}}}}
	rec := func() *Constant {
		return RecordConstant([]ConstField{{Name: "amount", Value: IntConstant(big.NewInt(7))}})
	}

	asCoin := Tagged(rec(), &Named{Def: coin})
	asLevel := Tagged(rec(), &Named{Def: level})
	untagged := rec()

	// Tagged is a pure copy: the original keeps no tag.
	if untagged.UnionTag != nil {
		t.Error("Tagged must not mutate the original value")
	}
	if asCoin.UnionTag == nil {
		t.Fatal("Tagged value should carry its tag")
	}

	// The same payload under different member tags is unequal.
	if ConstantsEqual(asCoin, asLevel) {
		t.Error("same payload, different tags must be unequal")
	}
	// A tag on one side only is unequal (the early-cutoff-safe side).
	if ConstantsEqual(asCoin, untagged) {
		t.Error("a tagged value must not equal the same payload untagged")
	}
	// Same member tag and same payload are equal.
	if !ConstantsEqual(asCoin, Tagged(rec(), &Named{Def: coin})) {
		t.Error("same tag and payload should be equal")
	}
	// A builtin-member tag compares by name.
	taggedErrA := Tagged(ErrorConstant("boom"), &Builtin{Name: "error"})
	taggedErrB := Tagged(ErrorConstant("boom"), &Builtin{Name: "error"})
	if !ConstantsEqual(taggedErrA, taggedErrB) {
		t.Error("two error values tagged error should be equal")
	}

	// Untagged drops the tag back to the bare payload; re-tagging the same member
	// is the identity (an equal value).
	if bare := Untagged(asCoin); bare.UnionTag != nil || !ConstantsEqual(bare, untagged) {
		t.Errorf("Untagged should yield the bare payload, got tag %v", bare.UnionTag)
	}
	if !ConstantsEqual(Tagged(asCoin, &Named{Def: coin}), asCoin) {
		t.Error("re-tagging the same member should be the identity")
	}
	// A nil tag or nil constant is a no-op.
	if Tagged(untagged, nil) != untagged {
		t.Error("Tagged with a nil tag should return the value unchanged")
	}
	if Tagged(nil, &Named{Def: coin}) != nil {
		t.Error("Tagged of a nil constant should stay nil")
	}
}

// TestNullConstant pins the null value: it renders as "null" (the String default
// would dereference a nil Int and panic without an explicit case), and two null
// values are equal — the single-inhabitant rule ConstantsEqual relies on so the
// folder and the early-cutoff agree.
func TestNullConstant(t *testing.T) {
	n := NullConstant()
	if got := n.String(); got != "null" {
		t.Errorf("String() = %q, want null", got)
	}
	if !ConstantsEqual(n, NullConstant()) {
		t.Error("two null constants should be equal")
	}
	if ConstantsEqual(n, IntConstant(big.NewInt(0))) {
		t.Error("null must not equal an integer")
	}
}

// TestRangeConstant pins the range value: it renders as range(start, end) (the
// String default would dereference a nil Int and panic without an explicit
// case), equality is by the bounds, and a range never equals a value of another
// kind — the contract ConstantsEqual's panic guard forces the new kind to honor.
func TestRangeConstant(t *testing.T) {
	r := RangeConstant(big.NewInt(0), big.NewInt(10))
	if got := r.String(); got != "range(0, 10)" {
		t.Errorf("String() = %q, want range(0, 10)", got)
	}
	if !ConstantsEqual(r, RangeConstant(big.NewInt(0), big.NewInt(10))) {
		t.Error("two ranges with the same bounds should be equal")
	}
	if ConstantsEqual(r, RangeConstant(big.NewInt(0), big.NewInt(9))) {
		t.Error("ranges with different bounds must not be equal")
	}
	// Differing kinds are never equal — a range against the integer it counts to.
	if ConstantsEqual(r, IntConstant(big.NewInt(10))) {
		t.Error("a range must not equal an integer")
	}
}

// TestConstantsEqualRelation pins that comparing relation values does not panic and
// uses chain identity: relations sharing a chain are equal, separately built ones are
// not. (Comparing relations is rejected by the analyzer, but the evaluator still folds
// such an expression on the way to its diagnostics, so equality must not panic.)
func TestConstantsEqualRelation(t *testing.T) {
	m := &TypeDef{Name: "Cards"}
	chain := &MasterRelation{Master: m}
	a := RelationConstant(chain)
	same := RelationConstant(chain)                       // same chain, distinct constant
	other := RelationConstant(&MasterRelation{Master: m}) // a separately built chain
	if !ConstantsEqual(a, same) {
		t.Error("relations sharing a chain should be equal")
	}
	if ConstantsEqual(a, other) {
		t.Error("relations with separately built chains should not be equal")
	}
}
