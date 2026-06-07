// This file is the hand-written half of the IR's text representation (F-4
// M3); text_gen.go carries the generated structural codec. By hand here is
// exactly what the generator cannot derive from the struct definitions:
//
//   - the Type codec: the type algebra includes an unexported singleton
//     (Invalid) and reference edges (Named.Def, App.Def), so its dispatch is
//     written out rather than generated — the generated code calls it through
//     the same writeTypeField/decodeTypeField names it would generate;
//   - the reference vocabulary: the IR is a graph, and a graph edge
//     (Reference.Target, Call.Resolved, Named.Def, ...) serializes as the
//     target's name — with the signature where overloads make a bare name
//     ambiguous — never as an embedding, so a cycle cannot recurse and
//     identity survives the trip as a name to re-resolve;
//   - the relink pass: unmarshaling rebuilds the trees with placeholder
//     targets carrying those names, and Link walks the module once, swapping
//     every placeholder for the declaration it names — two-pass exactly like
//     the semantic assembler's shells. References a module does not declare
//     (the prelude's types, a used file's constants) resolve through the
//     caller's Resolver.
//
// The unmarshaled module is detached by construction — the Syntax
// backpointers are tagged out of the format — which is what the P4 gate
// leans on: a module rebuilt from text physically cannot read the AST, so a
// fold that agrees with the original proves the backpointers carry no
// semantics (F-3's invariant, executed in CI).

package ir

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/internal/treetext"
)

// --- the reference vocabulary -------------------------------------------------

// refText renders a graph reference as its target's quoted name: a constant
// or type definition by name alone, a function as name(signature) to pick the
// overload, and a method as Owner.name(signature)[#kind] — the same
// vocabulary the relink resolves. A method's owner reads off its Owner edge
// (stamped by TypeDef.AttachMethods); a method built outside that channel
// renders "?", which Link rejects loudly rather than guessing.
func refText(v any) string {
	switch t := v.(type) {
	case *Const:
		if t == nil {
			return treetext.Nil
		}
		return strconv.Quote(t.Name)
	case *TypeDef:
		if t == nil {
			return treetext.Nil
		}
		return strconv.Quote(t.Name)
	case *Function:
		if t == nil {
			return treetext.Nil
		}
		if strings.Contains(t.Name, "(") {
			// An unlinked placeholder: its Name is the whole reference it was
			// decoded from, re-rendered verbatim so an unlinked module still
			// marshals faithfully.
			return strconv.Quote(t.Name)
		}
		return strconv.Quote(t.Name + paramSig(t.Params))
	case *Method:
		if t == nil {
			return treetext.Nil
		}
		if t.Owner == nil && strings.Contains(t.Name, "(") {
			// An unlinked placeholder: re-render the reference it carries.
			return strconv.Quote(t.Name)
		}
		owner := "?"
		if t.Owner != nil {
			owner = t.Owner.Name
		}
		ref := owner + "." + t.Name + paramSig(t.Params)
		if t.Kind != MethodNormal {
			ref += "#" + t.Kind.String()
		}
		return strconv.Quote(ref)
	default:
		panic(fmt.Sprintf("ir: refText: unsupported reference %T", v))
	}
}

// FunctionRef renders the reference form of a function — name(signature) —
// the vocabulary a Resolver receives for an external function and refText
// writes; exported so a resolver can index its candidates by the same string.
func FunctionRef(fn *Function) string {
	return fn.Name + paramSig(fn.Params)
}

// paramSig renders a signature's parameter types, the overload disambiguator:
// "(nint, string)". The rendering is Type.String, the same stable form
// diagnostics use, compared against candidates at relink.
func paramSig(params []Param) string {
	parts := make([]string, len(params))
	for i, p := range params {
		parts[i] = typeString(p.Type)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// unrefConst decodes a constant reference into a placeholder carrying the
// name, which Link swaps for the declaration.
func unrefConst(f treetext.Field) (*Const, error) {
	name, ok, err := refName(f)
	if !ok || err != nil {
		return nil, err
	}
	return &Const{Name: name}, nil
}

// unrefTypeDef decodes a type-definition reference into a placeholder.
func unrefTypeDef(f treetext.Field) (*TypeDef, error) {
	name, ok, err := refName(f)
	if !ok || err != nil {
		return nil, err
	}
	return &TypeDef{Name: name}, nil
}

// unrefFunction decodes a function reference — "name(sig)" — into a
// placeholder carrying the whole reference in Name.
func unrefFunction(f treetext.Field) (*Function, error) {
	ref, ok, err := refName(f)
	if !ok || err != nil {
		return nil, err
	}
	return &Function{Name: ref}, nil
}

// unrefMethod decodes a method reference — "Owner.name(sig)[#kind]" — into a
// placeholder carrying the whole reference in Name.
func unrefMethod(f treetext.Field) (*Method, error) {
	ref, ok, err := refName(f)
	if !ok || err != nil {
		return nil, err
	}
	return &Method{Name: ref}, nil
}

// refName reads a reference field's quoted name; ok is false for the nil
// marker.
func refName(f treetext.Field) (string, bool, error) {
	if f.Node == nil && f.Items == nil && f.Inline == treetext.Nil {
		return "", false, nil
	}
	name, err := treetext.String(f)
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}

// --- the type codec ------------------------------------------------------------

// typeHeads maps each type form to its element heading. The Invalid singleton
// gets its own heading: it is an unexported struct precisely so it stays a
// singleton, which is also why this codec is hand-written.
func typeHead(v Type) string {
	switch v.(type) {
	case *Builtin:
		return "Builtin"
	case *invalid:
		return "Invalid"
	case *Named:
		return "Named"
	case *Union:
		return "Union"
	case *Record:
		return "Record"
	case *Func:
		return "Func"
	case *TypeVar:
		return "TypeVar"
	case *App:
		return "App"
	case *SelfType:
		return "SelfType"
	default:
		panic(fmt.Sprintf("ir: typeHead: unhandled Type %T", v))
	}
}

// writeTypeField emits one Type-typed field: the nil marker or the type
// behind its heading.
func writeTypeField(w *treetext.Writer, depth int, name string, v Type) error {
	if v == nil {
		w.Line(depth, name+": "+treetext.Nil)
		return nil
	}
	w.Line(depth, name+": "+typeHead(v))
	return writeTypeFields(w, v, depth+1)
}

// writeTypeItem emits one Type-typed list item.
func writeTypeItem(w *treetext.Writer, depth int, v Type) error {
	if v == nil {
		w.Line(depth, treetext.Nil)
		return nil
	}
	w.Line(depth, typeHead(v))
	return writeTypeFields(w, v, depth+1)
}

// writeTypeFields emits a type's fields beneath its already-written heading.
func writeTypeFields(w *treetext.Writer, v Type, depth int) error {
	switch t := v.(type) {
	case *Builtin:
		w.Line(depth, "Name: "+strconv.Quote(t.Name))
	case *invalid, *SelfType:
		// No fields: the heading is the whole value.
	case *Named:
		w.Line(depth, "Def: "+refText(t.Def))
	case *Union:
		return writeTypeList(w, depth, "Members", t.Members)
	case *Record:
		if len(t.Fields) == 0 {
			w.Line(depth, "Fields: "+treetext.Nil)
			return nil
		}
		w.Line(depth, "Fields:")
		for i := range t.Fields {
			w.Line(depth+1, "Field")
			w.Line(depth+2, "Name: "+strconv.Quote(t.Fields[i].Name))
			if err := writeTypeField(w, depth+2, "Type", t.Fields[i].Type); err != nil {
				return err
			}
		}
	case *Func:
		if err := writeTypeList(w, depth, "Params", t.Params); err != nil {
			return err
		}
		return writeTypeField(w, depth, "Result", t.Result)
	case *TypeVar:
		w.Line(depth, "Name: "+strconv.Quote(t.Name))
		return writeTypeField(w, depth, "Bound", t.Bound)
	case *App:
		w.Line(depth, "Def: "+refText(t.Def))
		return writeTypeList(w, depth, "Args", t.Args)
	default:
		panic(fmt.Sprintf("ir: writeTypeFields: unhandled Type %T", v))
	}
	return nil
}

// writeTypeList emits a []Type field in the list form.
func writeTypeList(w *treetext.Writer, depth int, name string, items []Type) error {
	if len(items) == 0 {
		w.Line(depth, name+": "+treetext.Nil)
		return nil
	}
	w.Line(depth, name+":")
	for _, item := range items {
		if err := writeTypeItem(w, depth+1, item); err != nil {
			return err
		}
	}
	return nil
}

// decodeType decodes an element into its Type form, dispatching on the
// heading to the form's decoder.
func decodeType(e *treetext.Element) (Type, error) {
	switch e.Head {
	case "Builtin":
		return decodeBuiltinType(e)
	case "Invalid":
		if err := treetext.ExpectFields(e); err != nil {
			return nil, err
		}
		return Invalid, nil
	case "SelfType":
		if err := treetext.ExpectFields(e); err != nil {
			return nil, err
		}
		return &SelfType{}, nil
	case "Named":
		return decodeNamedType(e)
	case "Union":
		return decodeUnionType(e)
	case "Record":
		return decodeRecordType(e)
	case "Func":
		return decodeFuncType(e)
	case "TypeVar":
		return decodeTypeVar(e)
	case "App":
		return decodeAppType(e)
	default:
		return nil, fmt.Errorf("treetext: line %d: %s is not a known Type", e.Line, e.Head)
	}
}

// decodeBuiltinType decodes a primitive type by name.
func decodeBuiltinType(e *treetext.Element) (Type, error) {
	if err := treetext.ExpectFields(e, "Name"); err != nil {
		return nil, err
	}
	name, err := treetext.String(e.Fields[0])
	if err != nil {
		return nil, err
	}
	return &Builtin{Name: name}, nil
}

// decodeNamedType decodes a nominal reference into its placeholder.
func decodeNamedType(e *treetext.Element) (Type, error) {
	if err := treetext.ExpectFields(e, "Def"); err != nil {
		return nil, err
	}
	def, err := unrefTypeDef(e.Fields[0])
	if err != nil {
		return nil, err
	}
	return &Named{Def: def}, nil
}

// decodeUnionType decodes a union's member list.
func decodeUnionType(e *treetext.Element) (Type, error) {
	if err := treetext.ExpectFields(e, "Members"); err != nil {
		return nil, err
	}
	members, err := decodeTypeList(e.Fields[0])
	if err != nil {
		return nil, err
	}
	return &Union{Members: members}, nil
}

// decodeRecordType decodes an anonymous product type's fields.
func decodeRecordType(e *treetext.Element) (Type, error) {
	if err := treetext.ExpectFields(e, "Fields"); err != nil {
		return nil, err
	}
	fields, err := decodeRecordFields(e.Fields[0])
	if err != nil {
		return nil, err
	}
	return &Record{Fields: fields}, nil
}

// decodeFuncType decodes a function type's parameters and result.
func decodeFuncType(e *treetext.Element) (Type, error) {
	if err := treetext.ExpectFields(e, "Params", "Result"); err != nil {
		return nil, err
	}
	params, err := decodeTypeList(e.Fields[0])
	if err != nil {
		return nil, err
	}
	result, err := decodeTypeField(e.Fields[1])
	if err != nil {
		return nil, err
	}
	return &Func{Params: params, Result: result}, nil
}

// decodeTypeVar decodes a generic type parameter and its optional bound.
func decodeTypeVar(e *treetext.Element) (Type, error) {
	if err := treetext.ExpectFields(e, "Name", "Bound"); err != nil {
		return nil, err
	}
	name, err := treetext.String(e.Fields[0])
	if err != nil {
		return nil, err
	}
	bound, err := decodeTypeField(e.Fields[1])
	if err != nil {
		return nil, err
	}
	return &TypeVar{Name: name, Bound: bound}, nil
}

// decodeAppType decodes a generic application: its definition reference and
// arguments.
func decodeAppType(e *treetext.Element) (Type, error) {
	if err := treetext.ExpectFields(e, "Def", "Args"); err != nil {
		return nil, err
	}
	def, err := unrefTypeDef(e.Fields[0])
	if err != nil {
		return nil, err
	}
	args, err := decodeTypeList(e.Fields[1])
	if err != nil {
		return nil, err
	}
	return &App{Def: def, Args: args}, nil
}

// decodeTypeField decodes one Type-typed field: the nil marker or the type.
func decodeTypeField(f treetext.Field) (Type, error) {
	if f.Inline == treetext.Nil {
		return nil, nil
	}
	if f.Node == nil {
		return nil, fmt.Errorf("treetext: line %d: field %s: expected a type", f.Line, f.Name)
	}
	return decodeType(f.Node)
}

// decodeTypeList decodes a []Type field.
func decodeTypeList(f treetext.Field) ([]Type, error) {
	if f.Inline == treetext.Nil {
		return nil, nil
	}
	if f.Items == nil {
		return nil, fmt.Errorf("treetext: line %d: field %s: expected a list", f.Line, f.Name)
	}
	out := make([]Type, 0, len(f.Items))
	for i := range f.Items {
		item := &f.Items[i]
		if item.Head == treetext.Nil {
			out = append(out, nil)
			continue
		}
		t, err := decodeType(item)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// decodeRecordFields decodes a record type's field list.
func decodeRecordFields(f treetext.Field) ([]Field, error) {
	if f.Inline == treetext.Nil {
		return nil, nil
	}
	if f.Items == nil {
		return nil, fmt.Errorf("treetext: line %d: field %s: expected a list", f.Line, f.Name)
	}
	out := make([]Field, 0, len(f.Items))
	for i := range f.Items {
		item := &f.Items[i]
		if item.Head != "Field" {
			return nil, fmt.Errorf("treetext: line %d: %s is not a Field", item.Line, item.Head)
		}
		if err := treetext.ExpectFields(item, "Name", "Type"); err != nil {
			return nil, err
		}
		name, err := treetext.String(item.Fields[0])
		if err != nil {
			return nil, err
		}
		typ, err := decodeTypeField(item.Fields[1])
		if err != nil {
			return nil, err
		}
		out = append(out, Field{Name: name, Type: typ})
	}
	return out, nil
}

// --- the sealed-interface obligation -------------------------------------------

// Each Type form carries MarshalText so the sealed interface can embed
// encoding.TextMarshaler — the compile-time half of the format's
// exhaustiveness.

// MarshalText renders the type in the exact text form.
func (b *Builtin) MarshalText() ([]byte, error) { return marshalType(b) }

// MarshalText renders the type in the exact text form.
func (t *invalid) MarshalText() ([]byte, error) { return marshalType(t) }

// MarshalText renders the type in the exact text form.
func (n *Named) MarshalText() ([]byte, error) { return marshalType(n) }

// MarshalText renders the type in the exact text form.
func (u *Union) MarshalText() ([]byte, error) { return marshalType(u) }

// MarshalText renders the type in the exact text form.
func (r *Record) MarshalText() ([]byte, error) { return marshalType(r) }

// MarshalText renders the type in the exact text form.
func (f *Func) MarshalText() ([]byte, error) { return marshalType(f) }

// MarshalText renders the type in the exact text form.
func (v *TypeVar) MarshalText() ([]byte, error) { return marshalType(v) }

// MarshalText renders the type in the exact text form.
func (a *App) MarshalText() ([]byte, error) { return marshalType(a) }

// MarshalText renders the type in the exact text form.
func (t *SelfType) MarshalText() ([]byte, error) { return marshalType(t) }

// marshalType renders one type as a root element.
func marshalType(t Type) ([]byte, error) {
	var w treetext.Writer
	if err := writeTypeItem(&w, 0, t); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}
