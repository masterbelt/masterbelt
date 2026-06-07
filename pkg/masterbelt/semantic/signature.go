// This file holds the signature normalization behind duplicate-overload
// detection: two same-name methods or functions collide exactly when their
// resolved parameter types denote the same signature. signatureKey and
// funcSignatureKey render that key (normalizeKeyType canonicalizes self and
// type variables), and paramTypes/paramTypesOf render the human-readable list
// the duplicate diagnostics quote.
package semantic

import (
	"strconv"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// signatureKey renders a method's kind and parameter-type list as the duplicate-
// detection key: two same-name methods collide exactly when their kind and
// resolved parameter types denote the same signature. The kind is part of the
// key so the three name spaces stay apart — a getter `max` and an ordinary
// method `max()` both take no parameters, but live in different name spaces, so
// they must not dedup against each other (their collision, when illegal, is the
// accessor_collision check, not a dropped overload). Spellings of the same type
// are normalized — the enclosing type's own name reads as self (inside the impl
// they are the same type, and would otherwise both fit every call, making it
// permanently ambiguous), and the method's own type variables read by binding
// position (foo(a: T) and foo(a: U) are the same universal signature). The
// enclosing type's generic parameters keep their names: they are bound by the
// receiver, so distinct parameters are distinct types.
//
// The early-cutoff edit guard (TestEarlyCutoff*) rides on the kind being here:
// rewriting `get size(): T` to `size(): T` changes the key, so the edit
// re-resolves rather than reusing the stale getter typing.
func signatureKey(def *ir.TypeDef, m *ir.Method) string {
	bound := make(map[string]bool, len(def.Params))
	for _, p := range def.Params {
		bound[p.Name] = true
	}
	vars := map[string]int{}
	parts := make([]string, len(m.Params))
	for i, p := range m.Params {
		parts[i] = normalizeKeyType(def, p.Type, bound, vars)
	}
	return m.Kind.String() + "(" + strings.Join(parts, ", ") + ")"
}

// normalizeKeyType renders one parameter type for signatureKey, recursing
// through the composites so a nested self or type variable normalizes too.
func normalizeKeyType(def *ir.TypeDef, t ir.Type, bound map[string]bool, vars map[string]int) string {
	switch t := t.(type) {
	case nil:
		return "<nil>"
	case *ir.SelfType:
		return "self"
	case *ir.Named:
		if t.Def == def {
			return "self"
		}
		return t.String()
	case *ir.TypeVar:
		if bound[t.Name] {
			return t.Name
		}
		n, ok := vars[t.Name]
		if !ok {
			n = len(vars)
			vars[t.Name] = n
		}
		return "%" + strconv.Itoa(n)
	case *ir.App:
		name := ""
		if t.Def != nil {
			name = t.Def.Name
		}
		args := make([]string, len(t.Args))
		for i, a := range t.Args {
			args[i] = normalizeKeyType(def, a, bound, vars)
		}
		return name + "<" + strings.Join(args, ", ") + ">"
	case *ir.Func:
		params := make([]string, len(t.Params))
		for i, p := range t.Params {
			params[i] = normalizeKeyType(def, p, bound, vars)
		}
		return "fn(" + strings.Join(params, ", ") + "): " + normalizeKeyType(def, t.Result, bound, vars)
	case *ir.Union:
		members := make([]string, len(t.Members))
		for i, m := range t.Members {
			members[i] = normalizeKeyType(def, m, bound, vars)
		}
		return strings.Join(members, " | ")
	case *ir.Record:
		fields := make([]string, len(t.Fields))
		for i, f := range t.Fields {
			fields[i] = f.Name + ": " + normalizeKeyType(def, f.Type, bound, vars)
		}
		return "{ " + strings.Join(fields, ", ") + " }"
	default:
		return t.String()
	}
}

// paramTypes renders a method's parameter types as "a, b" for the
// duplicate-overload diagnostic.
func paramTypes(m *ir.Method) string {
	parts := make([]string, len(m.Params))
	for i, p := range m.Params {
		parts[i] = p.Type.String()
	}
	return strings.Join(parts, ", ")
}

// funcSignatureKey renders a function's parameter-type list as the duplicate-
// detection key: two same-name functions collide exactly when their resolved
// parameter types denote the same signature. Functions bind no receiver and no
// type variables, so the key is the normalized type list directly.
func funcSignatureKey(fn *ir.Function) string {
	vars := map[string]int{}
	parts := make([]string, len(fn.Params))
	for i, p := range fn.Params {
		parts[i] = normalizeKeyType(nil, p.Type, nil, vars)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// paramTypesOf renders a parameter list's types as "a, b" for the
// duplicate-overload diagnostic.
func paramTypesOf(params []ir.Param) string {
	parts := make([]string, len(params))
	for i, p := range params {
		if p.Type == nil {
			parts[i] = "<nil>"
			continue
		}
		parts[i] = p.Type.String()
	}
	return strings.Join(parts, ", ")
}
