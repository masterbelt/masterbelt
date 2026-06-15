// This file pins the builtin-surface contract's first layer: the bundled
// prelude source and the registry agree symbol for symbol, in both
// directions. The prelude→registry direction (every pure extern has an
// intrinsic per overload arm, every effectful extern has its registry record,
// every natively-backed primitive is declared `= builtin`) is enforced at
// load by validatePrelude; the tests here add the reverse direction — every
// registry native is reachable from a bundled declaration, so a dead native
// (registered, declared nowhere) fails the build — plus the per-symbol value
// checks the load-time validation does not make. A failure here means the
// toolchain build is broken; it can never implicate a user's program.
//
// It lives in package builtin_test (not builtin) because resolving the
// prelude needs package semantic, which imports builtin — the external test
// package breaks the cycle while keeping the pin with the registry it guards.
package builtin_test

import (
	"slices"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// preludeDefs loads and resolves the bundled prelude against the default
// registry — the same path the analyzer's universe takes, validation
// included.
func preludeDefs(t *testing.T) (*builtin.Registry, []*ir.TypeDef) {
	t.Helper()
	reg := builtin.Default()
	_, defs, err := semantic.LoadPrelude(reg)
	if err != nil {
		t.Fatalf("prelude failed to load: %v", err)
	}
	return reg, defs
}

// TestEveryIntrinsicIsDeclared is the dead-native check: every (type, method)
// the registry implements is declared as an extern method on that type in the
// bundled source. An intrinsic with no declaration is unreachable from any
// program — registering it was a mistake, or its declaration was lost.
func TestEveryIntrinsicIsDeclared(t *testing.T) {
	reg, defs := preludeDefs(t)
	declared := map[[2]string]bool{}
	for _, d := range defs {
		for _, m := range d.Methods {
			if m.Extern {
				declared[[2]string{d.Name, m.Name}] = true
			}
		}
	}
	for _, pair := range reg.IntrinsicSurface() {
		if !declared[pair] {
			t.Errorf("registry intrinsic %s.%s is declared nowhere in the bundled source (a dead native)", pair[0], pair[1])
		}
	}
}

// TestEveryEffectfulNativeIsDeclared is the dead-native check's effectful
// twin: every effectful native the registry records is declared as an
// effectful extern of the same kind, with the same effects, on that type in
// the bundled source — codegen's obligation list and the language surface
// cannot drift apart.
func TestEveryEffectfulNativeIsDeclared(t *testing.T) {
	reg, defs := preludeDefs(t)
	byName := map[string]*ir.TypeDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	for _, e := range reg.EffectfulNatives() {
		d := byName[e.Type]
		if d == nil {
			t.Errorf("effectful native %s.%s: type %s is not declared", e.Type, e.Name, e.Type)
			continue
		}
		found := false
		for _, m := range d.Methods {
			if m.Name == e.Name && m.Kind == e.Kind && m.Extern {
				found = true
				if len(m.Effects) == 0 {
					t.Errorf("effectful native %s.%s is declared with no effects", e.Type, e.Name)
				} else if !slices.Equal(m.Effects, e.Effects) {
					t.Errorf("effectful native %s.%s declares effects %v, registry records %v", e.Type, e.Name, m.Effects, e.Effects)
				}
			}
		}
		if !found {
			t.Errorf("effectful native %s.%s is declared nowhere in the bundled source", e.Type, e.Name)
		}
	}
}

// TestEveryPureExternIsBacked is the prelude→registry direction, asserted per
// symbol (validatePrelude enforces it at load; this spells the obligation out
// where the registry lives): every pure extern on a natively-backed primitive
// has an implementation, and every effectful extern has its registry record.
// Builtins the registry does not natively model — the collections and range,
// whose extern methods evaluate inside the folder itself — are pinned as an
// explicit, closed set: growing it is a deliberate act, not drift.
func TestEveryPureExternIsBacked(t *testing.T) {
	reg, defs := preludeDefs(t)
	// The builtins the registry does not natively model. The collections and range
	// are folder-implemented — declared `= builtin`, no NativeType, the folder
	// supplies their method semantics (collection/range intrinsics). The query
	// algebra (column/predicate/columns) is the other kind: its operators never
	// fold to a value at all — a column comparison is a predicate the query
	// lowering renders to SQL, not a constant the folder reduces — so it has no
	// NativeType and no intrinsic, and the query lowering, not the folder, gives it
	// meaning. Growing this set is a deliberate act, not drift.
	evalImplemented := map[string]bool{
		"list": true, "map": true, "range": true,
		"column": true, "predicate": true, "columns": true,
	}
	for _, d := range defs {
		if !d.Builtin {
			continue
		}
		if _, ok := reg.Native(d.Name); !ok {
			if !evalImplemented[d.Name] {
				t.Errorf("builtin type %s has no native descriptor and is not a pinned eval-implemented builtin", d.Name)
			}
			continue
		}
		for _, m := range d.Methods {
			if !m.Extern {
				continue
			}
			if len(m.Effects) > 0 {
				if _, ok := reg.Effectful(d.Name, m.Name, m.Kind); !ok {
					t.Errorf("effectful extern %s.%s has no registry record", d.Name, m.Name)
				}
				continue
			}
			if !reg.HasIntrinsic(d.Name, m.Name) {
				t.Errorf("pure extern %s.%s has no intrinsic", d.Name, m.Name)
			}
		}
	}
}

// TestEveryBuiltinBoundIsDeclared checks the `= builtin` associated constants
// agree per symbol: every declared one resolved to a value (a bound the
// registry supplies — a boundless one would have resolved to nothing), and
// every bound the registry can supply is declared on its type, so a sized
// primitive cannot lose its Max/Min silently.
func TestEveryBuiltinBoundIsDeclared(t *testing.T) {
	reg, defs := preludeDefs(t)
	for _, d := range defs {
		// Declared ⇒ supplied: a `= builtin` const that resolved to nothing
		// names a bound the registry does not back.
		for _, c := range d.Consts {
			if c.Builtin && c.Value == nil {
				t.Errorf("%s.%s is `= builtin` but resolved to no value", d.Name, c.Name)
			}
		}
		// Supplied ⇒ declared: a natively-backed sized integer exposes both
		// bounds.
		n, ok := reg.Native(d.Name)
		if !ok || !n.IsInteger() || n.Int.Bits == 0 {
			continue // unbounded (nint/nuint) or not an integer: no bound owed
		}
		for _, bound := range []string{"Max", "Min"} {
			found := false
			for _, c := range d.Consts {
				if c.Name == bound && c.Builtin {
					found = true
				}
			}
			if !found {
				t.Errorf("%s has a native %s bound but does not declare it `= builtin`", d.Name, bound)
			}
		}
	}
}
