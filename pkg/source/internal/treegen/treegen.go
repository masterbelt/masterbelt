// Package treegen generates the exact text representation (format v2) of a
// tree package: one writer and one strict decoder per node struct,
// derived from the struct definitions themselves so every exported field
// appears in the format by construction — a field added to a node is a field
// added to the text, never a silent omission.
//
// The generator reads the target package with go/types, collects the structs
// that implement the marker interface(s) plus every struct and interface
// reachable from their exported fields, and emits:
//
//   - write<T> per struct (the field lines), MarshalText per marker implementer
//   - decode<T> per struct, enforcing the canonical field order
//   - write<I>Field/write<I>Item and decode<I>/decode<I>Field per sealed
//     interface used as a field type, dispatching over its implementers
//   - UnmarshalText on the root struct(s)
//   - the treeStructs/writeTree/treeExcluded registry the field-sensitivity
//     pin walks
//
// Exclusions are explicit: unexported fields (the syntax backpointers) are
// out by construction, and an exported field is excluded only by a tree:"-"
// tag, which lands in treeExcluded so a test can pin the no-output decisions.
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"go/types"
	"maps"
	"reflect"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// treetextPath is the import path of the shared line/element grammar package.
const treetextPath = "github.com/masterbelt/masterbelt/pkg/source/internal/treetext"

// fieldKind classifies a struct field for the codec.
type fieldKind int

const (
	fieldBool    fieldKind = iota
	fieldString            // string
	fieldStrings           // []string
	fieldInt               // int, or a named type with underlying int (an enum)
	fieldInt64             // int64
	fieldBigInt            // *math/big.Int
	fieldNode              // *Struct or Interface
	fieldList              // []*Struct, []Struct, or []Interface
	fieldMap               // map[string]*Struct or map[string]Interface, sorted by key
	fieldRef               // a graph reference (tree:"ref"): serialized by name, relinked after decode
)

// fieldModel is one exported, non-excluded struct field.
type fieldModel struct {
	name    string
	kind    fieldKind
	elem    string // the struct/interface name for node kinds; the named int type for enums ("" for plain int)
	iface   bool   // whether elem names an interface
	byValue bool   // a fieldList of value structs ([]T rather than []*T)
}

// structModel is one tree struct.
type structModel struct {
	name     string
	fields   []fieldModel
	excluded []string // exported fields excluded by tree:"-"
	marshal  bool     // implements a marker interface: gets MarshalText
	root     bool     // gets UnmarshalText (and MarshalText, the root being the document)
}

// ifaceModel is one sealed interface used as a field type.
type ifaceModel struct {
	name         string
	implementers []string // the tree structs whose pointer implements it, sorted
}

// model is everything the emitter needs.
type model struct {
	pkgName string
	structs []*structModel // sorted by name
	ifaces  []*ifaceModel  // sorted by name
}

// config is the generation configuration beyond the target package: markers
// names the interface(s) whose implementers get MarshalText; roots names the
// struct(s) that get UnmarshalText (and MarshalText); custom names the
// interfaces whose dispatchers are hand-written (the generated code calls the
// same write<I>Field/write<I>Item/decode<I>/decode<I>Field names it would
// generate).
type config struct {
	markers []string
	roots   []string
	custom  map[string]bool
}

// Generate builds the codec source for the package in dir.
func Generate(dir string, cfg config) ([]byte, error) {
	pkg, err := load(dir)
	if err != nil {
		return nil, err
	}
	m, err := buildModel(pkg, cfg)
	if err != nil {
		return nil, err
	}
	src := emit(m)
	formatted, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("treegen: generated source does not format: %w\n%s", err, src)
	}
	return formatted, nil
}

// load type-checks the package in dir.
func load(dir string) (*types.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedDeps | packages.NeedImports,
		Dir:  dir,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, fmt.Errorf("treegen: load %s: %w", dir, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("treegen: load %s: got %d packages, want 1", dir, len(pkgs))
	}
	if len(pkgs[0].Errors) > 0 {
		return nil, fmt.Errorf("treegen: load %s: %w", dir, pkgs[0].Errors[0])
	}
	return pkgs[0].Types, nil
}

// buildModel discovers the tree structs and sealed interfaces of pkg: it seeds
// the marker implementers and roots (discoverStructs), walks their fields to pull
// in reachable structs and interfaces (walkFields), then resolves each
// interface's implementers (resolveIfaces) before assembling the sorted model.
func buildModel(pkg *types.Package, cfg config) (*model, error) {
	scope := pkg.Scope()

	markerIfaces, err := markerInterfaces(scope, pkg, cfg)
	if err != nil {
		return nil, err
	}

	structs, queue := discoverStructs(scope, cfg, markerIfaces)

	ifaces, err := walkFields(pkg, scope, cfg, structs, queue)
	if err != nil {
		return nil, err
	}

	for _, root := range cfg.roots {
		sm, ok := structs[root]
		if !ok {
			return nil, fmt.Errorf("treegen: root %s is not a discovered tree struct", root)
		}
		sm.root = true
	}

	if err := resolveIfaces(scope, structs, ifaces); err != nil {
		return nil, err
	}

	m := &model{pkgName: pkg.Name()}
	for _, name := range slices.Sorted(maps.Keys(structs)) {
		m.structs = append(m.structs, structs[name])
	}
	for _, name := range slices.Sorted(maps.Keys(ifaces)) {
		m.ifaces = append(m.ifaces, ifaces[name])
	}
	return m, nil
}

// markerInterfaces resolves the configured marker names to their interface types.
func markerInterfaces(scope *types.Scope, pkg *types.Package, cfg config) ([]*types.Interface, error) {
	markerIfaces := make([]*types.Interface, 0, len(cfg.markers))
	for _, name := range cfg.markers {
		obj := scope.Lookup(name)
		if obj == nil {
			return nil, fmt.Errorf("treegen: marker interface %s is not declared in %s", name, pkg.Path())
		}
		iface, ok := obj.Type().Underlying().(*types.Interface)
		if !ok {
			return nil, fmt.Errorf("treegen: marker %s is not an interface", name)
		}
		markerIfaces = append(markerIfaces, iface)
	}
	return markerIfaces, nil
}

// discoverStructs seeds the tree structs: every exported struct whose pointer
// implements a marker, plus any root that implements no marker (a document
// struct, seeded to anchor the reachability walk). It returns the seed map and
// the work queue of struct names whose fields are still to be walked.
func discoverStructs(scope *types.Scope, cfg config, markerIfaces []*types.Interface) (map[string]*structModel, []string) {
	structs := map[string]*structModel{}
	var queue []string
	for _, name := range slices.Sorted(slices.Values(scope.Names())) {
		obj, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || !obj.Exported() {
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			continue
		}
		if _, ok := named.Underlying().(*types.Struct); !ok {
			continue
		}
		ptr := types.NewPointer(named)
		for _, iface := range markerIfaces {
			if types.Implements(ptr, iface) {
				structs[name] = &structModel{name: name, marshal: true}
				queue = append(queue, name)
				break
			}
		}
		// A root that implements no marker (a document struct like a module)
		// is seeded too: it anchors the reachability walk.
		if structs[name] == nil && slices.Contains(cfg.roots, name) {
			structs[name] = &structModel{name: name}
			queue = append(queue, name)
		}
	}
	return structs, queue
}

// walkFields drains the queue, classifying each struct's exported fields and
// pulling in the structs and interfaces they reference (auxiliary entry types
// and dispatched interfaces). Newly referenced structs are appended to the queue
// for their own field walk; interfaces are recorded for implementer resolution.
// It returns the discovered interface models.
func walkFields(pkg *types.Package, scope *types.Scope, cfg config, structs map[string]*structModel, queue []string) (map[string]*ifaceModel, error) {
	ifaces := map[string]*ifaceModel{}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		sm := structs[name]
		named := scope.Lookup(name).Type().(*types.Named)
		st := named.Underlying().(*types.Struct)
		for i := range st.NumFields() {
			f := st.Field(i)
			if !f.Exported() {
				continue
			}
			if reflect.StructTag(st.Tag(i)).Get("tree") == "-" {
				sm.excluded = append(sm.excluded, f.Name())
				continue
			}
			fm, refs, err := classify(pkg, f, reflect.StructTag(st.Tag(i)))
			if err != nil {
				return nil, fmt.Errorf("treegen: %s.%s: %w", name, f.Name(), err)
			}
			sm.fields = append(sm.fields, *fm)
			queue = recordRefs(cfg, structs, ifaces, queue, refs)
		}
	}
	return ifaces, nil
}

// recordRefs records the types a field references: an interface (unless its codec
// is hand-written) is added to ifaces; a not-yet-seen struct is added to structs
// and appended to the queue. It returns the (possibly extended) queue.
func recordRefs(cfg config, structs map[string]*structModel, ifaces map[string]*ifaceModel, queue []string, refs []ref) []string {
	for _, ref := range refs {
		if ref.iface {
			if cfg.custom[ref.name] {
				continue // a hand-written codec: no dispatch generated
			}
			if _, ok := ifaces[ref.name]; !ok {
				ifaces[ref.name] = &ifaceModel{name: ref.name}
			}
			continue
		}
		if _, ok := structs[ref.name]; !ok {
			structs[ref.name] = &structModel{name: ref.name}
			queue = append(queue, ref.name)
		}
	}
	return queue
}

// resolveIfaces fills each interface model's implementers with the discovered
// structs whose pointer implements it, sorted, erroring on an interface with no
// in-package implementer.
func resolveIfaces(scope *types.Scope, structs map[string]*structModel, ifaces map[string]*ifaceModel) error {
	for name, im := range ifaces {
		iface := scope.Lookup(name).Type().Underlying().(*types.Interface)
		for sname := range structs {
			ptr := types.NewPointer(scope.Lookup(sname).Type())
			if types.Implements(ptr, iface) {
				im.implementers = append(im.implementers, sname)
			}
		}
		slices.Sort(im.implementers)
		if len(im.implementers) == 0 {
			return fmt.Errorf("treegen: interface %s has no struct implementers in the package", name)
		}
	}
	return nil
}

// ref is a type referenced by a field: a struct to pull into the model or an
// interface to dispatch over.
type ref struct {
	name  string
	iface bool
}

// classify maps a field's Go type onto the codec's kinds. It returns the
// field model and the in-package types the field references.
func classify(pkg *types.Package, f *types.Var, tag reflect.StructTag) (*fieldModel, []ref, error) {
	name := f.Name()
	if tag.Get("tree") == "ref" {
		// A graph reference: serialized by name through the hand-written
		// refText/unref<T> hooks, relinked after decode. The target struct is
		// not pulled into the model through this edge — a reference is a name,
		// not an embedding.
		ptr, ok := f.Type().(*types.Pointer)
		if !ok {
			return nil, nil, fmt.Errorf("tree:\"ref\" on a non-pointer field of type %s", f.Type())
		}
		n, ok := structName(pkg, ptr.Elem())
		if !ok {
			return nil, nil, fmt.Errorf("tree:\"ref\" target %s is not an in-package struct", ptr.Elem())
		}
		return &fieldModel{name: name, kind: fieldRef, elem: n}, nil, nil
	}
	switch t := f.Type().(type) {
	case *types.Basic:
		if fm := classifyBasic(name, t); fm != nil {
			return fm, nil, nil
		}
	case *types.Named:
		if fm, refs, ok := classifyNamed(pkg, name, t); ok {
			return fm, refs, nil
		}
	case *types.Pointer:
		if fm, refs, ok := classifyPointer(pkg, name, t); ok {
			return fm, refs, nil
		}
	case *types.Slice:
		if fm, refs, ok := classifySlice(pkg, name, t); ok {
			return fm, refs, nil
		}
	case *types.Map:
		if fm, refs, ok := classifyMap(pkg, name, t); ok {
			return fm, refs, nil
		}
	}
	return nil, nil, fmt.Errorf("unsupported field type %s (add a codec kind or exclude it with tree:\"-\")", f.Type())
}

// classifyBasic maps a basic-typed field to its codec kind, or nil for an
// unsupported basic kind (which classify turns into the unsupported-type error).
func classifyBasic(name string, t *types.Basic) *fieldModel {
	switch t.Kind() {
	case types.Bool:
		return &fieldModel{name: name, kind: fieldBool}
	case types.String:
		return &fieldModel{name: name, kind: fieldString}
	case types.Int:
		return &fieldModel{name: name, kind: fieldInt}
	case types.Int64:
		return &fieldModel{name: name, kind: fieldInt64}
	default:
		// Any other basic kind has no codec kind.
		return nil
	}
}

// classifyNamed maps a named-typed field: an in-package int-backed type is an
// enum (fieldInt with its type name), an in-package interface dispatches as a
// node. ok is false when the named type is neither.
func classifyNamed(pkg *types.Package, name string, t *types.Named) (*fieldModel, []ref, bool) {
	if basic, ok := t.Underlying().(*types.Basic); ok && basic.Kind() == types.Int && samePkg(pkg, t.Obj().Pkg()) {
		return &fieldModel{name: name, kind: fieldInt, elem: t.Obj().Name()}, nil, true
	}
	if _, ok := t.Underlying().(*types.Interface); ok && samePkg(pkg, t.Obj().Pkg()) {
		n := t.Obj().Name()
		return &fieldModel{name: name, kind: fieldNode, elem: n, iface: true}, []ref{{n, true}}, true
	}
	return nil, nil, false
}

// classifyPointer maps a pointer-typed field: a *big.Int is a bigint, a
// pointer to an in-package struct is a node. ok is false otherwise.
func classifyPointer(pkg *types.Package, name string, t *types.Pointer) (*fieldModel, []ref, bool) {
	if isBigInt(t.Elem()) {
		return &fieldModel{name: name, kind: fieldBigInt}, nil, true
	}
	if n, ok := structName(pkg, t.Elem()); ok {
		return &fieldModel{name: name, kind: fieldNode, elem: n}, []ref{{n, false}}, true
	}
	return nil, nil, false
}

// classifySlice maps a slice-typed field: []string is fieldStrings; a slice of
// struct pointers, in-package interfaces, or value structs is a fieldList. ok is
// false when the element type is none of these.
func classifySlice(pkg *types.Package, name string, t *types.Slice) (*fieldModel, []ref, bool) {
	switch e := t.Elem().(type) {
	case *types.Basic:
		if e.Kind() == types.String {
			return &fieldModel{name: name, kind: fieldStrings}, nil, true
		}
	case *types.Pointer:
		if n, ok := structName(pkg, e.Elem()); ok {
			return &fieldModel{name: name, kind: fieldList, elem: n}, []ref{{n, false}}, true
		}
	case *types.Named:
		if _, ok := e.Underlying().(*types.Interface); ok && samePkg(pkg, e.Obj().Pkg()) {
			n := e.Obj().Name()
			return &fieldModel{name: name, kind: fieldList, elem: n, iface: true}, []ref{{n, true}}, true
		}
		if n, ok := structName(pkg, e); ok {
			return &fieldModel{name: name, kind: fieldList, elem: n, byValue: true}, []ref{{n, false}}, true
		}
	}
	return nil, nil, false
}

// classifyMap maps a map-typed field keyed by string: a map to struct pointers
// or in-package interfaces is a fieldMap. ok is false for any other key or value
// type.
func classifyMap(pkg *types.Package, name string, t *types.Map) (*fieldModel, []ref, bool) {
	if basic, ok := t.Key().(*types.Basic); !ok || basic.Kind() != types.String {
		return nil, nil, false
	}
	switch v := t.Elem().(type) {
	case *types.Pointer:
		if n, ok := structName(pkg, v.Elem()); ok {
			return &fieldModel{name: name, kind: fieldMap, elem: n}, []ref{{n, false}}, true
		}
	case *types.Named:
		if _, ok := v.Underlying().(*types.Interface); ok && samePkg(pkg, v.Obj().Pkg()) {
			n := v.Obj().Name()
			return &fieldModel{name: name, kind: fieldMap, elem: n, iface: true}, []ref{{n, true}}, true
		}
	}
	return nil, nil, false
}

// isBigInt reports whether t is math/big.Int.
func isBigInt(t types.Type) bool {
	named, ok := t.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "math/big" && named.Obj().Name() == "Int"
}

// samePkg reports whether other is the generated package itself.
func samePkg(pkg, other *types.Package) bool {
	return other != nil && other.Path() == pkg.Path()
}

// structName returns the name of an in-package named struct type.
func structName(pkg *types.Package, t types.Type) (string, bool) {
	named, ok := t.(*types.Named)
	if !ok || !samePkg(pkg, named.Obj().Pkg()) {
		return "", false
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return "", false
	}
	return named.Obj().Name(), true
}

// --- emission ----------------------------------------------------------------

// emit renders the model as Go source.
func emit(m *model) []byte {
	var b bytes.Buffer
	p := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	needsMap := false
	for _, s := range m.structs {
		for _, f := range s.fields {
			if f.kind == fieldMap {
				needsMap = true
			}
		}
	}

	p("// Code generated by treegen. DO NOT EDIT.")
	p("//")
	p("// This file is the exact text representation (format v2) of the %s trees:", m.pkgName)
	p("// every exported, non-excluded field of every tree struct appears in the")
	p("// format, in declaration order — see the package documentation for the")
	p("// element grammar. Regenerate with `go generate ./...`.")
	p("")
	p("package %s", m.pkgName)
	p("")
	p("import (")
	p("\t\"fmt\"")
	if needsMap {
		p("\t\"maps\"")
		p("\t\"slices\"")
	}
	p("\t\"strconv\"")
	p("")
	p("\t%q", treetextPath)
	p(")")

	for _, s := range m.structs {
		emitWriter(p, s)
		emitDecoder(p, s)
		if s.marshal || s.root {
			emitMarshal(p, s)
		}
		if s.root {
			emitUnmarshal(p, s)
		}
	}
	for _, i := range m.ifaces {
		emitIface(p, i)
	}
	emitRegistry(p, m)
	return b.Bytes()
}

// emitWriter renders write<T>: the field lines beneath an already-written
// heading. Each field kind delegates to its own emit helper.
func emitWriter(p func(string, ...any), s *structModel) {
	p("")
	p("// write%s emits n's fields beneath an already-written heading line.", s.name)
	p("func write%s(w *treetext.Writer, n *%s, depth int) error {", s.name, s.name)
	for _, f := range s.fields {
		switch f.kind {
		case fieldBool, fieldString, fieldStrings, fieldInt, fieldInt64, fieldRef:
			emitWriteScalar(p, f)
		case fieldBigInt:
			emitWriteBigInt(p, f)
		case fieldNode:
			emitWriteNode(p, f)
		case fieldList:
			emitWriteList(p, f)
		case fieldMap:
			emitWriteMap(p, f)
		}
	}
	p("\treturn nil")
	p("}")
}

// emitWriteScalar emits one inline-value field line: the value rendered straight
// onto the field's "Name: " prefix.
func emitWriteScalar(p func(string, ...any), f fieldModel) {
	switch f.kind {
	case fieldBool:
		p("\tw.Line(depth, %q+strconv.FormatBool(n.%s))", f.name+": ", f.name)
	case fieldString:
		p("\tw.Line(depth, %q+strconv.Quote(n.%s))", f.name+": ", f.name)
	case fieldStrings:
		p("\tw.Line(depth, %q+treetext.QuoteStrings(n.%s))", f.name+": ", f.name)
	case fieldInt:
		p("\tw.Line(depth, %q+strconv.Itoa(int(n.%s)))", f.name+": ", f.name)
	case fieldInt64:
		p("\tw.Line(depth, %q+strconv.FormatInt(n.%s, 10))", f.name+": ", f.name)
	case fieldRef:
		p("\tw.Line(depth, %q+refText(n.%s))", f.name+": ", f.name)
	default:
		// The node-shaped kinds (big.Int, node, list, map) have their own
		// emitters; the caller's dispatch routes them there.
	}
}

// emitWriteBigInt emits a *big.Int field: the nil marker or its decimal string.
func emitWriteBigInt(p func(string, ...any), f fieldModel) {
	p("\tif n.%s == nil {", f.name)
	p("\t\tw.Line(depth, %q+treetext.Nil)", f.name+": ")
	p("\t} else {")
	p("\t\tw.Line(depth, %q+n.%s.String())", f.name+": ", f.name)
	p("\t}")
}

// emitWriteNode emits a node field: an interface field dispatches through
// write<I>Field; a struct field writes the nil marker or its heading and subtree.
func emitWriteNode(p func(string, ...any), f fieldModel) {
	if f.iface {
		p("\tif err := write%sField(w, depth, %q, n.%s); err != nil {", f.elem, f.name, f.name)
		p("\t\treturn err")
		p("\t}")
		return
	}
	p("\tif n.%s == nil {", f.name)
	p("\t\tw.Line(depth, %q+treetext.Nil)", f.name+": ")
	p("\t} else {")
	p("\t\tw.Line(depth, %q)", f.name+": "+f.elem)
	p("\t\tif err := write%s(w, n.%s, depth+1); err != nil {", f.elem, f.name)
	p("\t\t\treturn err")
	p("\t\t}")
	p("\t}")
}

// emitWriteList emits a list field: the nil marker for an empty slice, otherwise
// the field heading followed by each item — dispatched per element through
// write<I>Item (interface), by address (value struct), or by heading+subtree
// (struct pointer, with a per-item nil marker).
func emitWriteList(p func(string, ...any), f fieldModel) {
	p("\tif len(n.%s) == 0 {", f.name)
	p("\t\tw.Line(depth, %q+treetext.Nil)", f.name+": ")
	p("\t} else {")
	p("\t\tw.Line(depth, %q)", f.name+":")
	switch {
	case f.iface:
		p("\t\tfor _, item := range n.%s {", f.name)
		p("\t\t\tif err := write%sItem(w, depth+1, item); err != nil {", f.elem)
		p("\t\t\t\treturn err")
		p("\t\t\t}")
		p("\t\t}")
	case f.byValue:
		p("\t\tfor i := range n.%s {", f.name)
		p("\t\t\tw.Line(depth+1, %q)", f.elem)
		p("\t\t\tif err := write%s(w, &n.%s[i], depth+2); err != nil {", f.elem, f.name)
		p("\t\t\t\treturn err")
		p("\t\t\t}")
		p("\t\t}")
	default:
		p("\t\tfor _, item := range n.%s {", f.name)
		p("\t\t\tif item == nil {")
		p("\t\t\t\tw.Line(depth+1, treetext.Nil)")
		p("\t\t\t\tcontinue")
		p("\t\t\t}")
		p("\t\t\tw.Line(depth+1, %q)", f.elem)
		p("\t\t\tif err := write%s(w, item, depth+2); err != nil {", f.elem)
		p("\t\t\t\treturn err")
		p("\t\t\t}")
		p("\t\t}")
	}
	p("\t}")
}

// emitWriteMap emits a map field: the nil marker for an empty map, otherwise the
// field heading followed by one Entry (Key + Value) per key in sorted order, the
// value dispatched through write<I>Field (interface) or written inline (struct).
func emitWriteMap(p func(string, ...any), f fieldModel) {
	p("\tif len(n.%s) == 0 {", f.name)
	p("\t\tw.Line(depth, %q+treetext.Nil)", f.name+": ")
	p("\t} else {")
	p("\t\tw.Line(depth, %q)", f.name+":")
	p("\t\tfor _, key := range slices.Sorted(maps.Keys(n.%s)) {", f.name)
	p("\t\t\tw.Line(depth+1, \"Entry\")")
	p("\t\t\tw.Line(depth+2, \"Key: \"+strconv.Quote(key))")
	if f.iface {
		p("\t\t\tif err := write%sField(w, depth+2, \"Value\", n.%s[key]); err != nil {", f.elem, f.name)
		p("\t\t\t\treturn err")
		p("\t\t\t}")
	} else {
		p("\t\t\tif item := n.%s[key]; item == nil {", f.name)
		p("\t\t\t\tw.Line(depth+2, \"Value: \"+treetext.Nil)")
		p("\t\t\t} else {")
		p("\t\t\t\tw.Line(depth+2, %q)", "Value: "+f.elem)
		p("\t\t\t\tif err := write%s(w, item, depth+3); err != nil {", f.elem)
		p("\t\t\t\t\treturn err")
		p("\t\t\t\t}")
		p("\t\t\t}")
	}
	p("\t\t}")
	p("\t}")
}

// emitDecoder renders decode<T>: the strict, canonical-order field decoder.
func emitDecoder(p func(string, ...any), s *structModel) {
	names := make([]string, len(s.fields))
	for i, f := range s.fields {
		names[i] = fmt.Sprintf("%q", f.name)
	}
	p("")
	p("// decode%s builds a %s from its element.", s.name, s.name)
	p("func decode%s(e *treetext.Element) (*%s, error) {", s.name, s.name)
	if len(s.fields) == 0 {
		p("\tif err := treetext.ExpectFields(e); err != nil {")
	} else {
		p("\tif err := treetext.ExpectFields(e, %s); err != nil {", strings.Join(names, ", "))
	}
	p("\t\treturn nil, err")
	p("\t}")
	p("\tn := &%s{}", s.name)
	for i, f := range s.fields {
		switch f.kind {
		case fieldBool, fieldString, fieldStrings, fieldInt, fieldInt64, fieldBigInt, fieldRef:
			emitDecodeScalar(p, f, i)
		case fieldNode:
			emitDecodeNode(p, f, i)
		case fieldList:
			emitDecodeList(p, f, i)
		case fieldMap:
			emitDecodeMap(p, f, i)
		}
	}
	p("\treturn n, nil")
	p("}")
}

// emitDecodeScalar emits the decode of one inline-value field: parse the i-th
// field with the kind's treetext helper and assign it (an enum int is cast to its
// named type).
func emitDecodeScalar(p func(string, ...any), f fieldModel, i int) {
	assign := func(parse string) {
		p("\tif v, err := %s; err != nil {", parse)
		p("\t\treturn nil, err")
		p("\t} else {")
		p("\t\tn.%s = v", f.name)
		p("\t}")
	}
	switch f.kind {
	case fieldBool:
		assign(fmt.Sprintf("treetext.Bool(e.Fields[%d])", i))
	case fieldString:
		assign(fmt.Sprintf("treetext.String(e.Fields[%d])", i))
	case fieldStrings:
		assign(fmt.Sprintf("treetext.Strings(e.Fields[%d])", i))
	case fieldInt:
		cast := "v"
		if f.elem != "" {
			cast = f.elem + "(v)"
		}
		p("\tif v, err := treetext.Int(e.Fields[%d]); err != nil {", i)
		p("\t\treturn nil, err")
		p("\t} else {")
		p("\t\tn.%s = %s", f.name, cast)
		p("\t}")
	case fieldInt64:
		assign(fmt.Sprintf("treetext.Int64(e.Fields[%d])", i))
	case fieldBigInt:
		assign(fmt.Sprintf("treetext.BigInt(e.Fields[%d])", i))
	case fieldRef:
		assign(fmt.Sprintf("unref%s(e.Fields[%d])", f.elem, i))
	default:
		// The node-shaped kinds have their own decoders; the caller's
		// dispatch routes them there.
	}
}

// emitDecodeNode emits the decode of a node field: an interface field through
// decode<I>Field; a struct field as the nil marker, a head check, or a recursive
// decode of the i-th field.
func emitDecodeNode(p func(string, ...any), f fieldModel, i int) {
	if f.iface {
		p("\tif v, err := decode%sField(e.Fields[%d]); err != nil {", f.elem, i)
		p("\t\treturn nil, err")
		p("\t} else {")
		p("\t\tn.%s = v", f.name)
		p("\t}")
		return
	}
	p("\tswitch f := e.Fields[%d]; {", i)
	p("\tcase f.Inline == treetext.Nil:")
	p("\tcase f.Node == nil || f.Node.Head != %q:", f.elem)
	p("\t\treturn nil, fmt.Errorf(\"treetext: line %%d: field %%s: expected a %s\", f.Line, f.Name)", f.elem)
	p("\tdefault:")
	p("\t\tv, err := decode%s(f.Node)", f.elem)
	p("\t\tif err != nil {")
	p("\t\t\treturn nil, err")
	p("\t\t}")
	p("\t\tn.%s = v", f.name)
	p("\t}")
}

// emitDecodeList emits the decode of a list field: the nil marker for an absent
// list, otherwise each item decoded per element — through decode<I> (interface),
// by value (value struct, *v appended), or by struct pointer (with a per-item nil
// marker and head check).
func emitDecodeList(p func(string, ...any), f fieldModel, i int) {
	p("\tswitch f := e.Fields[%d]; {", i)
	p("\tcase f.Inline == treetext.Nil:")
	p("\tcase f.Items == nil:")
	p("\t\treturn nil, fmt.Errorf(\"treetext: line %%d: field %%s: expected a list\", f.Line, f.Name)")
	p("\tdefault:")
	switch {
	case f.iface:
		p("\t\tout := make([]%s, 0, len(f.Items))", f.elem)
	case f.byValue:
		p("\t\tout := make([]%s, 0, len(f.Items))", f.elem)
	default:
		p("\t\tout := make([]*%s, 0, len(f.Items))", f.elem)
	}
	p("\t\tfor j := range f.Items {")
	p("\t\t\titem := &f.Items[j]")
	if !f.byValue {
		p("\t\t\tif item.Head == treetext.Nil {")
		p("\t\t\t\tout = append(out, nil)")
		p("\t\t\t\tcontinue")
		p("\t\t\t}")
	}
	if f.iface {
		p("\t\t\tv, err := decode%s(item)", f.elem)
	} else {
		p("\t\t\tif item.Head != %q {", f.elem)
		p("\t\t\t\treturn nil, fmt.Errorf(\"treetext: line %%d: %%s is not a %s\", item.Line, item.Head)", f.elem)
		p("\t\t\t}")
		p("\t\t\tv, err := decode%s(item)", f.elem)
	}
	p("\t\t\tif err != nil {")
	p("\t\t\t\treturn nil, err")
	p("\t\t\t}")
	if f.byValue {
		p("\t\t\tout = append(out, *v)")
	} else {
		p("\t\t\tout = append(out, v)")
	}
	p("\t\t}")
	p("\t\tn.%s = out", f.name)
	p("\t}")
}

// emitDecodeMap emits the decode of a map field: the nil marker for an absent
// map, otherwise each Entry (Key + Value) decoded into the output map, the value
// through decode<I>Field (interface) or an inline struct decode.
func emitDecodeMap(p func(string, ...any), f fieldModel, i int) {
	p("\tswitch f := e.Fields[%d]; {", i)
	p("\tcase f.Inline == treetext.Nil:")
	p("\tcase f.Items == nil:")
	p("\t\treturn nil, fmt.Errorf(\"treetext: line %%d: field %%s: expected a list\", f.Line, f.Name)")
	p("\tdefault:")
	if f.iface {
		p("\t\tout := make(map[string]%s, len(f.Items))", f.elem)
	} else {
		p("\t\tout := make(map[string]*%s, len(f.Items))", f.elem)
	}
	p("\t\tfor j := range f.Items {")
	p("\t\t\titem := &f.Items[j]")
	p("\t\t\tif item.Head != \"Entry\" {")
	p("\t\t\t\treturn nil, fmt.Errorf(\"treetext: line %%d: %%s is not an Entry\", item.Line, item.Head)")
	p("\t\t\t}")
	p("\t\t\tif err := treetext.ExpectFields(item, \"Key\", \"Value\"); err != nil {")
	p("\t\t\t\treturn nil, err")
	p("\t\t\t}")
	p("\t\t\tkey, err := treetext.String(item.Fields[0])")
	p("\t\t\tif err != nil {")
	p("\t\t\t\treturn nil, err")
	p("\t\t\t}")
	if f.iface {
		p("\t\t\tval, err := decode%sField(item.Fields[1])", f.elem)
		p("\t\t\tif err != nil {")
		p("\t\t\t\treturn nil, err")
		p("\t\t\t}")
	} else {
		p("\t\t\tvar val *%s", f.elem)
		p("\t\t\tswitch vf := item.Fields[1]; {")
		p("\t\t\tcase vf.Inline == treetext.Nil:")
		p("\t\t\tcase vf.Node == nil || vf.Node.Head != %q:", f.elem)
		p("\t\t\t\treturn nil, fmt.Errorf(\"treetext: line %%d: field %%s: expected a %s\", vf.Line, vf.Name)", f.elem)
		p("\t\t\tdefault:")
		p("\t\t\t\tval, err = decode%s(vf.Node)", f.elem)
		p("\t\t\t\tif err != nil {")
		p("\t\t\t\t\treturn nil, err")
		p("\t\t\t\t}")
		p("\t\t\t}")
	}
	p("\t\t\tout[key] = val")
	p("\t\t}")
	p("\t\tn.%s = out", f.name)
	p("\t}")
}

// emitMarshal renders the MarshalText method the sealed interface obliges.
func emitMarshal(p func(string, ...any), s *structModel) {
	p("")
	p("// MarshalText renders the node and its subtree in the exact text form.")
	p("func (n *%s) MarshalText() ([]byte, error) {", s.name)
	p("\tvar w treetext.Writer")
	p("\tw.Line(0, %q)", s.name)
	p("\tif err := write%s(&w, n, 1); err != nil {", s.name)
	p("\t\treturn nil, err")
	p("\t}")
	p("\treturn w.Bytes(), nil")
	p("}")
}

// emitUnmarshal renders the root UnmarshalText.
func emitUnmarshal(p func(string, ...any), s *structModel) {
	p("")
	p("// UnmarshalText parses the exact text form back into the node. The result")
	p("// is detached: the unexported syntax backpointers stay nil by construction.")
	p("func (n *%s) UnmarshalText(data []byte) error {", s.name)
	p("\te, err := treetext.Parse(data)")
	p("\tif err != nil {")
	p("\t\treturn err")
	p("\t}")
	p("\tif e.Head != %q {", s.name)
	p("\t\treturn fmt.Errorf(\"treetext: line %%d: root is %%s, want %s\", e.Line, e.Head)", s.name)
	p("\t}")
	p("\tv, err := decode%s(e)", s.name)
	p("\tif err != nil {")
	p("\t\treturn err")
	p("\t}")
	p("\t*n = *v")
	p("\treturn nil")
	p("}")
}

// emitIface renders the per-interface write and decode dispatchers.
func emitIface(p func(string, ...any), i *ifaceModel) {
	emitIfaceWriteField(p, i)
	emitIfaceWriteItem(p, i)
	emitIfaceDecode(p, i)
	emitIfaceDecodeField(p, i)
}

// emitIfaceWriteField renders write<I>Field, which emits one interface-typed
// field: the nil marker or the concrete node behind its heading, dispatched over
// the implementers.
func emitIfaceWriteField(p func(string, ...any), i *ifaceModel) {
	p("")
	p("// write%sField emits one %s-typed field: the nil marker or the concrete", i.name, i.name)
	p("// node behind its heading.")
	p("func write%sField(w *treetext.Writer, depth int, name string, v %s) error {", i.name, i.name)
	p("\tswitch n := v.(type) {")
	p("\tcase nil:")
	p("\t\tw.Line(depth, name+\": \"+treetext.Nil)")
	p("\t\treturn nil")
	for _, impl := range i.implementers {
		p("\tcase *%s:", impl)
		p("\t\tif n == nil {")
		p("\t\t\tw.Line(depth, name+\": \"+treetext.Nil)")
		p("\t\t\treturn nil")
		p("\t\t}")
		p("\t\tw.Line(depth, name+\": %s\")", impl)
		p("\t\treturn write%s(w, n, depth+1)", impl)
	}
	p("\tdefault:")
	p("\t\treturn fmt.Errorf(\"treetext: field %%s: unsupported %s %%T\", name, v)", i.name)
	p("\t}")
	p("}")
}

// emitIfaceWriteItem renders write<I>Item, which emits one interface-typed list
// item: the nil marker line or the concrete element, dispatched over the
// implementers.
func emitIfaceWriteItem(p func(string, ...any), i *ifaceModel) {
	p("")
	p("// write%sItem emits one %s-typed list item: the nil marker line or the", i.name, i.name)
	p("// concrete element.")
	p("func write%sItem(w *treetext.Writer, depth int, v %s) error {", i.name, i.name)
	p("\tswitch n := v.(type) {")
	p("\tcase nil:")
	p("\t\tw.Line(depth, treetext.Nil)")
	p("\t\treturn nil")
	for _, impl := range i.implementers {
		p("\tcase *%s:", impl)
		p("\t\tif n == nil {")
		p("\t\t\tw.Line(depth, treetext.Nil)")
		p("\t\t\treturn nil")
		p("\t\t}")
		p("\t\tw.Line(depth, %q)", impl)
		p("\t\treturn write%s(w, n, depth+1)", impl)
	}
	p("\tdefault:")
	p("\t\treturn fmt.Errorf(\"treetext: unsupported %s %%T\", v)", i.name)
	p("\t}")
	p("}")
}

// emitIfaceDecode renders decode<I>, which decodes an element into its interface
// implementation by dispatching on the element head.
func emitIfaceDecode(p func(string, ...any), i *ifaceModel) {
	p("")
	p("// decode%s decodes an element into its %s implementation.", i.name, i.name)
	p("func decode%s(e *treetext.Element) (%s, error) {", i.name, i.name)
	p("\tswitch e.Head {")
	for _, impl := range i.implementers {
		p("\tcase %q:", impl)
		p("\t\treturn decode%s(e)", impl)
	}
	p("\tdefault:")
	p("\t\treturn nil, fmt.Errorf(\"treetext: line %%d: %%s is not a known %s\", e.Line, e.Head)", i.name)
	p("\t}")
	p("}")
}

// emitIfaceDecodeField renders decode<I>Field, which decodes one interface-typed
// field: the nil marker or the node through decode<I>.
func emitIfaceDecodeField(p func(string, ...any), i *ifaceModel) {
	p("")
	p("// decode%sField decodes one %s-typed field: the nil marker or the node.", i.name, i.name)
	p("func decode%sField(f treetext.Field) (%s, error) {", i.name, i.name)
	p("\tif f.Inline == treetext.Nil {")
	p("\t\treturn nil, nil")
	p("\t}")
	p("\tif f.Node == nil {")
	p("\t\treturn nil, fmt.Errorf(\"treetext: line %%d: field %%s: expected a node\", f.Line, f.Name)")
	p("\t}")
	p("\treturn decode%s(f.Node)", i.name)
	p("}")
}

// emitRegistry renders the test-support registry: the struct list, the any
// dispatcher, and the explicit-exclusion manifest.
func emitRegistry(p func(string, ...any), m *model) {
	p("")
	p("// treeStructs lists one typed nil pointer per tree struct, for the")
	p("// field-sensitivity pin.")
	p("var treeStructs = []any{")
	for _, s := range m.structs {
		p("\t(*%s)(nil),", s.name)
	}
	p("}")
	p("")
	p("// writeTree dispatches a tree struct to its writer (test support); the")
	p("// bool reports whether v is a known tree struct.")
	p("func writeTree(w *treetext.Writer, v any, depth int) (bool, error) {")
	p("\tswitch n := v.(type) {")
	for _, s := range m.structs {
		p("\tcase *%s:", s.name)
		p("\t\tw.Line(depth, %q)", s.name)
		p("\t\treturn true, write%s(w, n, depth+1)", s.name)
	}
	p("\tdefault:")
	p("\t\treturn false, nil")
	p("\t}")
	p("}")
	p("")
	p("// treeExcluded records the exported fields excluded from the format by a")
	p("// tree:\"-\" tag, per struct: the explicit no-output decisions, pinned by")
	p("// test. Unexported fields (the syntax backpointers) are excluded by")
	p("// construction and do not appear here.")
	p("var treeExcluded = map[string][]string{")
	for _, s := range m.structs {
		if len(s.excluded) == 0 {
			continue
		}
		quoted := make([]string, len(s.excluded))
		for i, name := range s.excluded {
			quoted[i] = fmt.Sprintf("%q", name)
		}
		p("\t%q: {%s},", s.name, strings.Join(quoted, ", "))
	}
	p("}")
}
