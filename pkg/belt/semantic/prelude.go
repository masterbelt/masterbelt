package semantic

import (
	"bytes"
	"fmt"
	"slices"
	"sync"

	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// builtins is everything an analysis types against: the registry — the
// primitives' value ranges and the native implementations of their operator
// methods — and the prelude surface, the exported types of the prelude file.
// Every file's universe layers the surface beneath its imports, as if each
// file began with `use * from "builtin.belt"`.
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

// LoadPrelude analyzes the embedded prelude — the one file that declares the
// primitives in the language. Its names bootstrap against the registry's
// native definitions; the analysis is validated against the registry, so the
// in-language declarations and the native descriptors cannot drift.
//
// It returns the prelude's exported types — the surface every analyzed file
// implicitly imports — together with every resolved definition (for
// installing into the registry, where value ranges and intrinsics live).
func LoadPrelude(reg *builtin.Registry) (map[string]*ir.TypeDef, []*ir.TypeDef, error) {
	src := builtin.PreludeSource()
	doc := abstract.NewDocument(src)
	diags := make([]diagnostic.Diagnostic, 0, len(doc.Concrete().LexDiagnostics())+len(doc.Diagnostics()))
	diags = append(diags, doc.Concrete().LexDiagnostics()...)
	diags = append(diags, doc.Diagnostics()...)
	if len(diags) > 0 {
		// The fragment banners in the concatenated source place the line.
		return nil, nil, fmt.Errorf("prelude %s:%d: %s", builtin.PreludeEntry, lineOf(src, diags[0].Offset), diags[0].Message)
	}

	// The prelude is one scope: the belt/ fragments concatenate into this one
	// file, so a use would name a file that does not exist.
	if f := doc.File(); len(f.Uses) > 0 {
		return nil, nil, fmt.Errorf("prelude: use %q — the prelude is a single file and imports nothing", f.Uses[0].Path)
	}

	id := FileID(builtin.PreludeEntry)
	files := map[FileID]*ast.File{id: doc.File()}
	uses := map[FileID]map[*ast.UseDecl]FileID{id: {}}
	q := newDirectQueries(files, uses, builtins{reg: reg, prelude: registryTypes(reg)})
	defs := q.typeDefs(id)
	if err := validatePrelude(reg, defs); err != nil {
		return nil, nil, err
	}

	// The surface is what the prelude exports; every registry primitive must
	// be on it, or the implicit import would hide a primitive the runtime
	// backs.
	surface := q.exportsOf(id).types
	for _, name := range reg.Names() {
		if _, ok := surface[name]; !ok {
			return nil, nil, fmt.Errorf("prelude: %s does not export registry primitive %q", builtin.PreludeEntry, name)
		}
	}
	return surface, defs, nil
}

// lineOf is the 1-based line that a byte offset into src falls on.
func lineOf(src []byte, offset int) int {
	if offset > len(src) {
		offset = len(src)
	}
	return 1 + bytes.Count(src[:offset], []byte("\n"))
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
	if err := validatePreludePrimitives(reg, declared); err != nil {
		return err
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
			if err := validateExternMethod(reg, d, native, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// validatePreludePrimitives checks that every registry primitive is declared in
// the prelude as a `= builtin`.
func validatePreludePrimitives(reg *builtin.Registry, declared map[string]*ir.TypeDef) error {
	for _, name := range reg.Names() {
		d, ok := declared[name]
		if !ok {
			return fmt.Errorf("prelude: registry primitive %q is not declared", name)
		}
		if !d.Builtin {
			return fmt.Errorf("prelude: %q is declared but not as `= builtin`", name)
		}
	}
	return nil
}

// validateExternMethod checks one extern method on a natively-backed primitive:
// an effectful extern must have a registered effectful native whose effects
// match the declared ones, and a pure extern must have a registered intrinsic
// for its argument kinds (or, when a parameter has no constant kind, by name).
// A non-extern method has no obligation.
func validateExternMethod(reg *builtin.Registry, d *ir.TypeDef, native *builtin.NativeType, m *ir.Method) error {
	if !m.Extern {
		return nil
	}
	// An effectful extern has no compile-time implementation by definition (it
	// is never folded); its obligation is the registry's effectful-native record
	// — the explicit, per-symbol promise that a target's codegen supplies it —
	// and the declared effects must be the recorded ones.
	if len(m.Effects) > 0 {
		e, ok := reg.Effectful(d.Name, m.Name, m.Kind)
		if !ok {
			return fmt.Errorf("prelude: %s.%s is an effectful extern but the registry records no effectful native for it", d.Name, m.Name)
		}
		if !slices.Equal(e.Effects, m.Effects) {
			return fmt.Errorf("prelude: %s.%s declares effects %v but the registry records %v", d.Name, m.Name, m.Effects, e.Effects)
		}
		return nil
	}
	kinds, known := paramKinds(reg, native, m.Params)
	if !known {
		// A parameter type the evaluator has no constant kind for (none exists
		// on a natively-backed primitive today): the per-name check is the
		// strongest claim left.
		if !reg.HasIntrinsic(d.Name, m.Name) {
			return fmt.Errorf("prelude: %s.%s is extern but the registry has no intrinsic for it", d.Name, m.Name)
		}
		return nil
	}
	if _, ok := reg.Intrinsic(d.Name, m.Name, kinds); !ok {
		return fmt.Errorf("prelude: %s.%s is extern but the registry has no intrinsic for its argument kinds", d.Name, m.Name)
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
	case n.Null:
		return ir.ConstNull, true
	default:
		return 0, false
	}
}
