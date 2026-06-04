package semantic

import (
	"fmt"
	"sync"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/ir"
)

// universe is the registry every analysis types against: the standard registry
// with the prelude loaded and installed, so primitive method signatures come
// from the prelude. It is built once and shared, since it is read-only during
// analysis. If the prelude fails to load (a build-time bug the prelude test
// catches), analysis degrades to the bootstrap registry rather than crashing.
var (
	universeOnce sync.Once
	universeReg  *builtin.Registry
)

func universe() *builtin.Registry {
	universeOnce.Do(func() {
		reg := builtin.Default()
		if defs, err := LoadPrelude(reg); err == nil {
			reg.Install(defs)
		}
		universeReg = reg
	})
	return universeReg
}

// LoadPrelude parses the embedded prelude — the masterbelt source that declares
// the builtin primitives — resolves its type declarations, and validates them
// against the registry. It returns the prelude's type definitions (the builtin
// primitives and the numeric union aliases) and an error if a prelude file does
// not parse or if its declarations do not agree with the registry.
//
// The prelude declares the primitives' types and operator-method signatures in
// the language; the registry supplies their value ranges and the native
// implementations of those methods. Validating one against the other is what
// keeps the in-language declarations and the native descriptors from drifting.
func LoadPrelude(reg *builtin.Registry) ([]*ir.TypeDef, error) {
	var defs []*ir.TypeDef
	for _, src := range builtin.PreludeSources() {
		doc := abstract.NewDocument(src.Content)
		var diags []diagnostic.Diagnostic
		diags = append(diags, doc.Concrete().LexDiagnostics()...)
		diags = append(diags, doc.Diagnostics()...)
		if len(diags) > 0 {
			return nil, fmt.Errorf("prelude %s: %s", src.Name, diags[0].Message)
		}
		defs = append(defs, resolveTypes(doc.File(), reg, nil, nil)...)
	}
	if err := validatePrelude(reg, defs); err != nil {
		return nil, err
	}
	return defs, nil
}

// validatePrelude checks that the prelude and the registry agree: every registry
// primitive is declared in the prelude as a `= builtin`, and every extern method
// a builtin declares has a registered native implementation.
func validatePrelude(reg *builtin.Registry, defs []*ir.TypeDef) error {
	declared := make(map[string]*ir.TypeDef, len(defs))
	for _, d := range defs {
		declared[d.Name] = d
	}

	for _, name := range reg.Names() {
		d, ok := declared[name]
		if !ok {
			return fmt.Errorf("prelude: registry primitive %q is not declared", name)
		}
		if !d.Builtin {
			return fmt.Errorf("prelude: %q is declared but not as `= builtin`", name)
		}
	}

	for _, d := range defs {
		// Only primitives the registry natively backs are required to have
		// intrinsics. A builtin the registry does not yet model (the generic
		// collections, list and map) is declared in the prelude but not yet
		// natively implemented, which is allowed.
		if !d.Builtin {
			continue
		}
		if _, native := reg.Native(d.Name); !native {
			continue
		}
		for _, m := range d.Methods {
			if !m.Extern {
				continue
			}
			if _, ok := reg.Intrinsic(d.Name, m.Name); !ok {
				return fmt.Errorf("prelude: %s.%s is extern but the registry has no intrinsic for it", d.Name, m.Name)
			}
		}
	}
	return nil
}
