package builtin

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestIntrinsicKindDispatch checks the overload dispatch: an implementation
// registered for an exact argument-kind signature wins over the kind-agnostic
// one, which in turn answers any kinds no exact signature claims.
func TestIntrinsicKindDispatch(t *testing.T) {
	r := Default()
	one := ir.IntConstant(big.NewInt(1))

	mark := func(s string) Intrinsic {
		return func(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
			return ir.StringConstant(s)
		}
	}
	// An overloaded method: one implementation per argument-kind signature,
	// the way datetime/duration registers add and sub.
	r.registerIntrinsic("nint", "overloaded", []ir.ConstKind{ir.ConstInt}, mark("nint"))
	r.registerIntrinsic("nint", "overloaded", []ir.ConstKind{ir.ConstBool}, mark("bool"))

	cases := []struct {
		name  string
		kinds []ir.ConstKind
		want  string
	}{
		{"nint signature", []ir.ConstKind{ir.ConstInt}, "nint"},
		{"bool signature", []ir.ConstKind{ir.ConstBool}, "bool"},
	}
	for _, tc := range cases {
		fn, ok := r.Intrinsic("nint", "overloaded", tc.kinds)
		if !ok {
			t.Fatalf("%s: Intrinsic not found", tc.name)
		}
		if got := fn(one, nil); got.Str != tc.want {
			t.Errorf("%s: dispatched to %q, want %q", tc.name, got.Str, tc.want)
		}
	}

	// Kinds no signature claims: not found (no kind-agnostic fallback here).
	if _, ok := r.Intrinsic("nint", "overloaded", []ir.ConstKind{ir.ConstString}); ok {
		t.Error("unclaimed kinds resolved an exact-signature method")
	}
	if _, ok := r.Intrinsic("nint", "overloaded", nil); ok {
		t.Error("an empty signature resolved an exact-signature method")
	}

	// A kind-agnostic implementation joins the set as the fallback for
	// whatever the exact signatures do not claim.
	r.registerIntrinsic("nint", "overloaded", nil, mark("any"))
	fn, ok := r.Intrinsic("nint", "overloaded", []ir.ConstKind{ir.ConstString})
	if !ok {
		t.Fatal("fallback: Intrinsic not found")
	}
	if got := fn(one, nil); got.Str != "any" {
		t.Errorf("fallback: dispatched to %q, want any", got.Str)
	}
	// The exact signatures still win over the fallback.
	if fn, _ := r.Intrinsic("nint", "overloaded", []ir.ConstKind{ir.ConstBool}); fn(one, nil).Str != "bool" {
		t.Error("an exact signature lost to the fallback")
	}
}

// TestIntrinsicSingleImplementation checks the un-overloaded path every
// standard primitive uses: the kind-agnostic implementation answers any
// argument kinds, and HasIntrinsic mirrors what the prelude validation needs.
func TestIntrinsicSingleImplementation(t *testing.T) {
	r := Default()

	fn, ok := r.Intrinsic("nint", "add", []ir.ConstKind{ir.ConstInt})
	if !ok {
		t.Fatal("nint.add not found")
	}
	a, b := ir.IntConstant(big.NewInt(2)), ir.IntConstant(big.NewInt(3))
	if got := fn(a, []*ir.Constant{b}); got == nil || got.Int.Int64() != 5 {
		t.Errorf("nint.add(2, 3) = %v, want 5", got)
	}

	// The same implementation answers mismatched kinds (and declines inside,
	// returning no value) — the type rules already rejected such a program.
	fn, ok = r.Intrinsic("nint", "add", []ir.ConstKind{ir.ConstBool})
	if !ok {
		t.Fatal("nint.add with a bool argument must still dispatch")
	}
	if got := fn(a, []*ir.Constant{ir.BoolConstant(true)}); got != nil {
		t.Errorf("nint.add(2, true) = %v, want nil", got)
	}

	if !r.HasIntrinsic("nint", "add") {
		t.Error("HasIntrinsic(nint, add) = false, want true")
	}
	if r.HasIntrinsic("nint", "frobnicate") {
		t.Error("HasIntrinsic(nint, frobnicate) = true, want false")
	}
}
