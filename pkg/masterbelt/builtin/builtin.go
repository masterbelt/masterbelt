// Package builtin is the registry of native primitive types: the single
// injection point that supplies the value ranges and operator implementations
// the type system and the evaluator need, keyed on a builtin's name.
//
// A primitive's structure — its name and its operator method signatures — is
// described here as an *ir.TypeDef, so the type system resolves a primitive's
// methods through exactly the same path as a user type's. Its native semantics —
// value range and the evaluation of each operator — is described by a NativeType
// and a set of Intrinsics. Adding a primitive (int128, a float, a string) is
// therefore a matter of registering a descriptor here and declaring it in the
// prelude; nothing in the type system hardcodes the set of primitives or their
// ranges.
//
// The prelude (pkg/masterbelt/builtin/belt) declares the same primitives in
// masterbelt syntax. Package semantic loads it and validates it against this
// registry, so the two cannot drift.
package builtin

import (
	"sort"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Registry is a set of native primitives. Build the standard one with Default.
type Registry struct {
	order      []string
	defs       map[string]*ir.TypeDef
	natives    map[string]*NativeType
	intrinsics map[string]map[string][]intrinsicEntry
	effectful  []EffectfulNative
}

// EffectfulNative records a native the registry supplies no compile-time
// implementation for: an effectful routine — the root of an effect — whose
// implementation a target's codegen must provide. A pure native is an
// Intrinsic (folded at compile time); an effectful one is never folded, so its
// registration here is the explicit, CI-pinned obligation the prelude's
// `extern ... fn <effect>` declaration is validated against per symbol.
type EffectfulNative struct {
	Type    string        // the owning primitive's name
	Name    string        // the method or static fn name
	Kind    ir.MethodKind // ir.MethodStatic for a static fn, ir.MethodNormal for an instance method
	Effects []string      // the declared effects, in declaration order
}

// Effectful returns the effectful-native record of the named routine of the
// given kind on the named primitive — what the prelude validation asks for an
// extern declaration that carries effects.
func (r *Registry) Effectful(typeName, name string, kind ir.MethodKind) (EffectfulNative, bool) {
	for _, e := range r.effectful {
		if e.Type == typeName && e.Name == name && e.Kind == kind {
			return e, true
		}
	}
	return EffectfulNative{}, false
}

// EffectfulNatives returns every effectful native in registration order — the
// finite set of implementations a target's codegen owes, which the builtin
// tests pin against the prelude's declarations.
func (r *Registry) EffectfulNatives() []EffectfulNative {
	return r.effectful
}

// registerEffectful records one effectful native.
func (r *Registry) registerEffectful(e EffectfulNative) {
	r.effectful = append(r.effectful, e)
}

// Lookup returns the type definition of the primitive named name.
func (r *Registry) Lookup(name string) (*ir.TypeDef, bool) {
	d, ok := r.defs[name]
	return d, ok
}

// Native returns the native descriptor of the primitive named name.
func (r *Registry) Native(name string) (*NativeType, bool) {
	n, ok := r.natives[name]
	return n, ok
}

// Intrinsic returns the native implementation of the named method on the
// named primitive type for the given argument kinds: the implementation
// registered for exactly those kinds, or the method's kind-agnostic sole
// implementation. The evaluator dispatches here with the evaluated arguments'
// kinds, mirroring the argument-type overload selection the type rules made —
// the two agree because an overload's parameter types determine its argument
// kinds.
func (r *Registry) Intrinsic(typeName, method string, kinds []ir.ConstKind) (Intrinsic, bool) {
	var fallback Intrinsic
	for _, e := range r.intrinsics[typeName][method] {
		if e.kinds == nil {
			if fallback == nil {
				fallback = e.fn
			}
			continue
		}
		if kindsEqual(e.kinds, kinds) {
			return e.fn, true
		}
	}
	if fallback != nil {
		return fallback, true
	}
	return nil, false
}

// HasIntrinsic reports whether the named method has any native implementation
// on the named primitive — what the prelude validation asks, where no
// argument values exist yet.
func (r *Registry) HasIntrinsic(typeName, method string) bool {
	return len(r.intrinsics[typeName][method]) > 0
}

// IntrinsicSurface returns every (type, method) pair that has at least one
// native implementation, sorted by type then method. It is the registry side
// of the per-symbol prelude agreement: the builtin tests walk it to prove
// every native is reachable from a bundled-source declaration (a dead native
// fails the build) while the prelude validation proves the reverse.
func (r *Registry) IntrinsicSurface() [][2]string {
	var out [][2]string
	for typeName, ms := range r.intrinsics {
		for method := range ms {
			out = append(out, [2]string{typeName, method})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// kindsEqual reports whether the two kind signatures are the same.
func kindsEqual(a, b []ir.ConstKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Names returns the natively-backed primitive names in registration order. It is
// unaffected by Install, so it stays the set of primitives validation must cover.
func (r *Registry) Names() []string { return r.order }

// Defs returns every type definition currently in scope, by name: the bootstrap
// primitives together with whatever the prelude installed (its numeric aliases
// and the generic collections). Unlike Names — the natively-backed primitives in
// registration order — this reflects Install, so it is the set a client (e.g. an
// editor completing a type name) should see.
func (r *Registry) Defs() []*ir.TypeDef {
	out := make([]*ir.TypeDef, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Install makes the given type definitions — typically the prelude's, after it
// has been loaded and validated — the source for type lookup, replacing the
// bootstrap definitions matched by name and adding any new ones (the prelude's
// aliases and collections). The native descriptors and intrinsics are unchanged,
// so a primitive's value range and operator implementations still come from the
// registry while its method signatures now come from the prelude.
func (r *Registry) Install(defs []*ir.TypeDef) {
	for _, d := range defs {
		if d.Name != "" {
			r.defs[d.Name] = d
		}
	}
}

// --- the standard registry --------------------------------------------------

var integerSpecs = []struct {
	name string
	kind IntKind
}{
	{"nint", IntKind{Signed: true, Bits: 0}},
	{"sbyte", IntKind{Signed: true, Bits: 8}},
	{"short", IntKind{Signed: true, Bits: 16}},
	{"int", IntKind{Signed: true, Bits: 32}},
	{"long", IntKind{Signed: true, Bits: 64}},
	{"nuint", IntKind{Signed: false, Bits: 0}},
	{"byte", IntKind{Signed: false, Bits: 8}},
	{"ushort", IntKind{Signed: false, Bits: 16}},
	{"uint", IntKind{Signed: false, Bits: 32}},
	{"ulong", IntKind{Signed: false, Bits: 64}},
}

// Default returns the standard registry: the integer family, bool, string, and
// null.
func Default() *Registry {
	r := &Registry{
		defs:       map[string]*ir.TypeDef{},
		natives:    map[string]*NativeType{},
		intrinsics: map[string]map[string][]intrinsicEntry{},
	}
	for _, spec := range integerSpecs {
		kind := spec.kind
		ii := integerIntrinsics()
		if !kind.Signed {
			// The unsigned integers declare no negation in the prelude, so a
			// neg intrinsic on them would be a dead native — registered but
			// reachable from no declaration (the agreement test pins this).
			delete(ii, "neg")
		}
		r.register(spec.name, &NativeType{Name: spec.name, Int: &kind}, integerMethods(), ii)
	}
	r.register("bool", &NativeType{Name: "bool", Bool: true}, booleanMethods(), booleanIntrinsics())
	r.register("string", &NativeType{Name: "string", Str: true}, stringMethods(), stringIntrinsics())
	r.register("null", &NativeType{Name: "null", Null: true}, nil, nil)
	r.register("error", &NativeType{Name: "error", Err: true}, errorMethods(), errorIntrinsics())

	// datetime: the comparisons and the single-signature add are kind-
	// agnostic; sub is overloaded by the argument's kind — another instant
	// yields the span between them, a duration the earlier instant.
	dtI := millisComparisons(ir.ConstDatetime)
	dtI["add"] = binaryMillis(ir.ConstDatetime, ir.ConstDuration, checkedMillis(addMillis, ir.DatetimeConstant))
	r.register(NameDatetime, &NativeType{Name: NameDatetime, Datetime: true}, datetimeMethods(), dtI)
	r.registerIntrinsic(NameDatetime, "sub", []ir.ConstKind{ir.ConstDatetime},
		binaryMillis(ir.ConstDatetime, ir.ConstDatetime, checkedMillis(subMillis, ir.DurationConstant)))
	r.registerIntrinsic(NameDatetime, "sub", []ir.ConstKind{ir.ConstDuration},
		binaryMillis(ir.ConstDatetime, ir.ConstDuration, checkedMillis(subMillis, ir.DatetimeConstant)))
	// datetime.now(): the current instant — the first effectful native, the
	// root of nondet. It deliberately has no compile-time implementation: a
	// nondet value does not reproduce, so folding it would be wrong by
	// definition; a target's codegen supplies it at runtime.
	r.registerEffectful(EffectfulNative{Type: NameDatetime, Name: "now", Kind: ir.MethodStatic, Effects: []string{"nondet"}})

	// duration: add is overloaded by the argument's kind — another span sums,
	// a datetime yields the instant the span after it.
	drI := millisComparisons(ir.ConstDuration)
	drI["sub"] = binaryMillis(ir.ConstDuration, ir.ConstDuration, checkedMillis(subMillis, ir.DurationConstant))
	drI["mul"] = mulDuration
	r.register("duration", &NativeType{Name: "duration", Duration: true}, durationMethods(), drI)
	r.registerIntrinsic("duration", "add", []ir.ConstKind{ir.ConstDuration},
		binaryMillis(ir.ConstDuration, ir.ConstDuration, checkedMillis(addMillis, ir.DurationConstant)))
	r.registerIntrinsic("duration", "add", []ir.ConstKind{ir.ConstDatetime},
		binaryMillis(ir.ConstDuration, ir.ConstDatetime, checkedMillis(addMillis, ir.DatetimeConstant)))
	return r
}

func (r *Registry) register(name string, native *NativeType, methods []*ir.Method, intrinsics map[string]Intrinsic) {
	r.order = append(r.order, name)
	def := &ir.TypeDef{
		Name:    name,
		Public:  true,
		Body:    &ir.Builtin{Name: name},
		Builtin: true,
	}
	def.AttachMethods(methods...)
	r.defs[name] = def
	r.natives[name] = native
	for method, fn := range intrinsics {
		r.registerIntrinsic(name, method, nil, fn)
	}
}

// registerIntrinsic adds one native implementation of method on the named
// primitive, dispatching on the given argument kinds. nil kinds registers the
// method's kind-agnostic sole implementation; an overloaded method registers
// one entry per argument-kind signature.
func (r *Registry) registerIntrinsic(typeName, method string, kinds []ir.ConstKind, fn Intrinsic) {
	ms, ok := r.intrinsics[typeName]
	if !ok {
		ms = map[string][]intrinsicEntry{}
		r.intrinsics[typeName] = ms
	}
	ms[method] = append(ms[method], intrinsicEntry{kinds: kinds, fn: fn})
}
