package semantic

import (
	"fmt"
	"path"
	"sync"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// builtins is everything an analysis types against: the registry — the
// primitives' value ranges and the native implementations of their operator
// methods — and the prelude surface, the exported types of the prelude
// project's entry barrel. Every file's universe layers the surface beneath
// its imports, as if each file began with `use * from "builtin.belt"`.
type builtins struct {
	reg     *builtin.Registry
	prelude map[string]*ir.TypeDef
}

// universe is the builtins every analysis types against, built once and
// shared (read-only during analysis). If the prelude fails to load (a
// build-time bug the prelude test catches), the surface degrades to the
// bootstrap registry's definitions rather than crashing.
var (
	universeOnce sync.Once
	universeVal  builtins
)

func universe() builtins {
	universeOnce.Do(func() {
		reg := builtin.Default()
		u := builtins{reg: reg, prelude: registryTypes(reg)}
		if surface, defs, err := LoadPrelude(reg); err == nil {
			reg.Install(defs)
			u.prelude = surface
		}
		universeVal = u
	})
	return universeVal
}

// registryTypes is a registry's definitions by name — the bootstrap surface
// the prelude itself resolves against, and the fallback when it fails to
// load.
func registryTypes(reg *builtin.Registry) map[string]*ir.TypeDef {
	defs := reg.Defs()
	out := make(map[string]*ir.TypeDef, len(defs))
	for _, d := range defs {
		if d.Name != "" {
			out[d.Name] = d
		}
	}
	return out
}

// LoadPrelude analyzes the embedded prelude as the project it is: each module
// declares primitives in the language, and the entry barrel (builtin.belt)
// re-exports them all. Cross-module names bootstrap against the registry's
// native definitions; the analysis is validated against the registry, so the
// in-language declarations and the native descriptors cannot drift.
//
// It returns the barrel's exported types — the surface every analyzed file
// implicitly imports — together with every resolved definition (for
// installing into the registry, where value ranges and intrinsics live).
func LoadPrelude(reg *builtin.Registry) (map[string]*ir.TypeDef, []*ir.TypeDef, error) {
	sources := builtin.PreludeSources()
	files := make(map[FileID]*ast.File, len(sources))
	uses := map[FileID]map[*ast.UseDecl]FileID{}
	ids := make([]FileID, 0, len(sources))
	for _, src := range sources {
		doc := abstract.NewDocument(src.Content)
		var diags []diagnostic.Diagnostic
		diags = append(diags, doc.Concrete().LexDiagnostics()...)
		diags = append(diags, doc.Diagnostics()...)
		if len(diags) > 0 {
			return nil, nil, fmt.Errorf("prelude %s: %s", src.Name, diags[0].Message)
		}
		id := FileID(src.Name)
		files[id] = doc.File()
		ids = append(ids, id)
	}

	// Wire the use declarations among the embedded files — the project
	// layer's path rule (relative to the importer), without a disk.
	for id, f := range files {
		table := map[*ast.UseDecl]FileID{}
		for _, u := range f.Uses {
			target := FileID(path.Join(path.Dir(string(id)), u.Path))
			if _, ok := files[target]; !ok {
				return nil, nil, fmt.Errorf("prelude %s: use %q names no prelude file", id, u.Path)
			}
			table[u] = target
		}
		uses[id] = table
	}

	q := newDirectQueries(files, uses, builtins{reg: reg, prelude: registryTypes(reg)})
	var defs []*ir.TypeDef
	for _, id := range ids {
		defs = append(defs, q.typeDefs(id)...)
	}
	if err := validatePrelude(reg, defs); err != nil {
		return nil, nil, err
	}

	// The surface is what the entry barrel exports; every registry primitive
	// must be on it, or the implicit import would hide a primitive the
	// runtime backs.
	surface := q.exportsOf(FileID(builtin.PreludeEntry)).types
	for _, name := range reg.Names() {
		if _, ok := surface[name]; !ok {
			return nil, nil, fmt.Errorf("prelude: %s does not re-export registry primitive %q", builtin.PreludeEntry, name)
		}
	}
	return surface, defs, nil
}

// validatePrelude checks that the prelude and the registry agree: every registry
// primitive is declared in the prelude as a `= builtin`, and every extern method
// a builtin declares has a registered native implementation — per overload
// signature, so a declared arm whose intrinsic was never registered (a
// duration.add(at: datetime) without its kind-keyed implementation) cannot
// drift past the build.
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
		native, ok := reg.Native(d.Name)
		if !ok {
			continue
		}
		for _, m := range d.Methods {
			if !m.Extern {
				continue
			}
			kinds, known := paramKinds(reg, native, m.Params)
			if !known {
				// A parameter type the evaluator has no constant kind for
				// (none exists on a natively-backed primitive today): the
				// per-name check is the strongest claim left.
				if !reg.HasIntrinsic(d.Name, m.Name) {
					return fmt.Errorf("prelude: %s.%s is extern but the registry has no intrinsic for it", d.Name, m.Name)
				}
				continue
			}
			if _, ok := reg.Intrinsic(d.Name, m.Name, kinds); !ok {
				return fmt.Errorf("prelude: %s.%s is extern but the registry has no intrinsic for its argument kinds", d.Name, m.Name)
			}
		}
	}
	return nil
}

// paramKinds maps an extern method's parameter types to the argument kinds
// the evaluator will dispatch with: self is the receiver's own kind, any
// other primitive its native kind. known is false when a parameter has no
// constant kind to dispatch on (a type variable, a function, a collection).
func paramKinds(reg *builtin.Registry, recv *builtin.NativeType, params []ir.Param) ([]ir.ConstKind, bool) {
	kinds := make([]ir.ConstKind, len(params))
	for i, p := range params {
		var n *builtin.NativeType
		switch t := p.Type.(type) {
		case *ir.SelfType:
			n = recv
		case *ir.Builtin:
			n, _ = reg.Native(t.Name)
		case *ir.Named:
			if t.Def != nil && t.Def.Builtin {
				n, _ = reg.Native(t.Def.Name)
			}
		}
		k, ok := nativeKind(n)
		if !ok {
			return nil, false
		}
		kinds[i] = k
	}
	return kinds, true
}

// nativeKind is the constant kind a native primitive's values carry.
func nativeKind(n *builtin.NativeType) (ir.ConstKind, bool) {
	switch {
	case n == nil:
		return 0, false
	case n.IsInteger():
		return ir.ConstInt, true
	case n.Bool:
		return ir.ConstBool, true
	case n.Str:
		return ir.ConstString, true
	case n.Datetime:
		return ir.ConstDatetime, true
	case n.Duration:
		return ir.ConstDuration, true
	case n.Err:
		return ir.ConstError, true
	default:
		return 0, false
	}
}
