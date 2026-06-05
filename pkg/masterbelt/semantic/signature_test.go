package semantic

// These tests exercise signature-key normalization behind duplicate-overload
// detection (signature.go): two same-name methods or functions collide exactly
// when their resolved parameter types denote the same signature, with self,
// the enclosing type's own name, and method type variables canonicalized.

import (
	"testing"
)

func TestDuplicateOverload(t *testing.T) {
	// The same name with the same parameter types is a true redeclaration:
	// the first wins, the repeat is reported.
	src := `pub type Score = int32 impl {
  pub fn merge(points: self): self {
    return self + points
  }
  pub fn merge(other: self): self {
    return self
  }
}
const Base: Score = 100
const X = Base.merge(5)
`
	m, diags := analyze(src)
	if got := codes(diags); len(got) != 1 || got[0] != CodeDuplicateOverload {
		t.Fatalf("codes = %v, want [duplicate_overload]", got)
	}
	if len(m.Types[0].Methods) != 1 {
		t.Errorf("Score has %d methods, want 1 (the repeat dropped)", len(m.Types[0].Methods))
	}
	// The call still resolves through the surviving first declaration.
	if got := m.Consts[1].Type.String(); got != "Score" {
		t.Errorf("X type = %s, want Score", got)
	}
}

func TestDuplicateOverloadNormalizesSpellings(t *testing.T) {
	// self and the type's own name denote the same type inside the impl, so
	// an overload differing only in that spelling is a redeclaration — were
	// both kept, every call would fit both and be permanently ambiguous.
	src := `pub type Score = int32 impl {
  pub fn merge(points: self): self {
    return self + points
  }
  pub fn merge(points: Score): self {
    return self
  }
}
const Base: Score = 100
const X = Base.merge(Base)
`
	m, diags := analyze(src)
	if got := codes(diags); len(got) != 1 || got[0] != CodeDuplicateOverload {
		t.Fatalf("self vs named: codes = %v, want [duplicate_overload]", got)
	}
	if got := m.Consts[1].Type.String(); got != "Score" {
		t.Errorf("X type = %s, want Score (resolved through the first declaration)", got)
	}

	// Two method-introduced type variables are the same universal signature
	// whatever they are named.
	src = `pub type Box = int32 impl {
  pub extern fn wrap(value: T): bool
  pub extern fn wrap(value: U): bool
}
`
	_, diags = analyze(src)
	if got := codes(diags); len(got) != 1 || got[0] != CodeDuplicateOverload {
		t.Fatalf("alpha-equivalent vars: codes = %v, want [duplicate_overload]", got)
	}
}

func TestDuplicateOverloadKeepsBodiesAligned(t *testing.T) {
	// Dropping the duplicate must not shift the pairing of the remaining
	// declarations with their resolved signatures: flag's body still checks
	// against flag's bool result, not against a neighbour's.
	src := `pub type T = int32 impl {
  pub fn a(x: self): self {
    return self + x
  }
  pub fn a(y: self): self {
    return self
  }
  pub fn flag(): bool {
    return self > 0
  }
}
`
	_, diags := analyze(src)
	if got := codes(diags); len(got) != 1 || got[0] != CodeDuplicateOverload {
		t.Fatalf("codes = %v, want [duplicate_overload] alone", got)
	}
}

func TestFuncOverloadDuplicateStillCallable(t *testing.T) {
	// A repeated signature reports at its declaration and is dropped: the
	// first declaration keeps working, the call stays unambiguous.
	src := "fn f(): int -> 1\nfn f(): int -> 2\nconst A = f()\n"
	m, diags := analyze(src)
	if got := codes(diags); len(got) != 1 || got[0] != CodeDuplicateFuncOverload {
		t.Fatalf("codes = %v, want [duplicate_func_overload]", got)
	}
	if len(m.Funcs) != 1 {
		t.Errorf("module funcs = %d, want the duplicate dropped", len(m.Funcs))
	}
	if m.Consts[0].Type.String() != "int" {
		t.Errorf("A type = %s, want int (the first overload)", m.Consts[0].Type)
	}
}
