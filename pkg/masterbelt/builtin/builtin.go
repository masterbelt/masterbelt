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
	"math"
	"math/big"
	"sort"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// IntKind describes an integer primitive's representation. Bits == 0 means
// arbitrary precision: a signed one then has no bounds, an unsigned one only a
// lower bound of zero.
type IntKind struct {
	Signed bool
	Bits   uint
}

// bounds returns the inclusive value range of the integer kind. A nil bound
// means "unbounded on that side".
func (k IntKind) bounds() (min, max *big.Int) {
	one := big.NewInt(1)
	if k.Bits == 0 {
		if k.Signed {
			return nil, nil
		}
		return big.NewInt(0), nil
	}
	if k.Signed {
		half := new(big.Int).Lsh(one, k.Bits-1)
		return new(big.Int).Neg(half), new(big.Int).Sub(half, one)
	}
	return big.NewInt(0), new(big.Int).Sub(new(big.Int).Lsh(one, k.Bits), one)
}

// NativeType is the native description of a primitive: its numeric kind (for an
// integer), or a flag marking the boolean, string, null, datetime, or duration
// type.
type NativeType struct {
	Name     string
	Int      *IntKind // non-nil for an integer primitive
	Bool     bool     // the boolean type
	Str      bool     // the string type
	Null     bool     // the null type
	Datetime bool     // the datetime type (a UTC instant in epoch milliseconds)
	Duration bool     // the duration type (a span in milliseconds)
}

// IsInteger reports whether the primitive is an integer type.
func (n *NativeType) IsInteger() bool { return n.Int != nil }

// IsBoolean reports whether the primitive is the boolean type.
func (n *NativeType) IsBoolean() bool { return n.Bool }

// IsString reports whether the primitive is the string type.
func (n *NativeType) IsString() bool { return n.Str }

// Fits reports whether v is within the primitive's value range. A non-integer
// primitive (or an arbitrary-precision integer) accepts any value within its
// (possibly half-open) range.
func (n *NativeType) Fits(v *big.Int) bool {
	if n.Int == nil {
		return true
	}
	min, max := n.Int.bounds()
	if min != nil && v.Cmp(min) < 0 {
		return false
	}
	if max != nil && v.Cmp(max) > 0 {
		return false
	}
	return true
}

// Intrinsic is the native implementation of an extern method: it computes the
// method's value from the receiver and argument constants (all guaranteed
// non-nil by the caller), or returns nil when the operation has no value — a
// type-incorrect program, or a division by zero.
type Intrinsic func(recv *ir.Constant, args []*ir.Constant) *ir.Constant

// intrinsicEntry is one native implementation of an extern method: the
// function and the argument-kind signature it dispatches on. A nil kinds list
// marks a kind-agnostic implementation — the match for any arguments when no
// exact signature claims them (every un-overloaded method registers this way).
type intrinsicEntry struct {
	kinds []ir.ConstKind
	fn    Intrinsic
}

// Registry is a set of native primitives. Build the standard one with Default.
type Registry struct {
	order      []string
	defs       map[string]*ir.TypeDef
	natives    map[string]*NativeType
	intrinsics map[string]map[string][]intrinsicEntry
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

// boolType is the shared boolean primitive type used in operator-method
// signatures (the result of the comparison and equality methods).
var boolType ir.Type = &ir.Builtin{Name: "bool"}

func self() ir.Type { return &ir.SelfType{} }

// externMethod builds an extern operator-method signature: its parameter types
// and result type, with no body (the implementation is an Intrinsic).
func externMethod(name string, result ir.Type, params ...ir.Type) *ir.Method {
	ps := make([]ir.Param, len(params))
	for i, p := range params {
		ps[i] = ir.Param{Name: "other", Type: p}
	}
	return &ir.Method{Name: name, Public: true, Extern: true, Params: ps, Result: result}
}

// integerMethods is the operator-method signature set shared by every integer
// primitive: arithmetic returns self, comparisons and equality return bool, and
// the unary signs return self.
func integerMethods() []*ir.Method {
	return []*ir.Method{
		externMethod("pos", self()),
		externMethod("neg", self()),
		externMethod("add", self(), self()),
		externMethod("sub", self(), self()),
		externMethod("mul", self(), self()),
		externMethod("div", self(), self()),
		externMethod("rem", self(), self()),
		externMethod("eql", boolType, self()),
		externMethod("neq", boolType, self()),
		externMethod("lt", boolType, self()),
		externMethod("lteq", boolType, self()),
		externMethod("gt", boolType, self()),
		externMethod("gteq", boolType, self()),
	}
}

// booleanMethods is the operator-method signature set of the boolean primitive:
// logical ops return self, equality returns bool, and not returns self.
func booleanMethods() []*ir.Method {
	return []*ir.Method{
		externMethod("not", self()),
		externMethod("anan", self(), self()),
		externMethod("oror", self(), self()),
		externMethod("eql", boolType, self()),
		externMethod("neq", boolType, self()),
	}
}

// stringMethods is the operator-method signature set of the string primitive:
// add concatenates (returning self), and equality and the lexicographic
// comparisons return bool. It mirrors the prelude's string.belt.
func stringMethods() []*ir.Method {
	return []*ir.Method{
		externMethod("add", self(), self()),
		externMethod("eql", boolType, self()),
		externMethod("neq", boolType, self()),
		externMethod("lt", boolType, self()),
		externMethod("lteq", boolType, self()),
		externMethod("gt", boolType, self()),
		externMethod("gteq", boolType, self()),
	}
}

// datetimeType and durationType are the cross-referenced builtin types in the
// datetime/duration operator signatures: the two interoperate (dt ± dr,
// dt - dt, dr + dt), so each names the other in its overloads.
var (
	datetimeType ir.Type = &ir.Builtin{Name: "datetime"}
	durationType ir.Type = &ir.Builtin{Name: "duration"}
	intType      ir.Type = &ir.Builtin{Name: "int"}
)

// comparisonMethods is the equality and ordering signature set shared by the
// datetime and duration primitives: both compare against self and return bool.
func comparisonMethods() []*ir.Method {
	return []*ir.Method{
		externMethod("eql", boolType, self()),
		externMethod("neq", boolType, self()),
		externMethod("lt", boolType, self()),
		externMethod("lteq", boolType, self()),
		externMethod("gt", boolType, self()),
		externMethod("gteq", boolType, self()),
	}
}

// datetimeMethods is the operator-method signature set of the datetime
// primitive. sub is overloaded by argument type: another instant yields the
// span between them, a duration yields the earlier instant. It mirrors the
// prelude's datetime.belt.
func datetimeMethods() []*ir.Method {
	return append(comparisonMethods(),
		externMethod("add", self(), durationType),
		externMethod("sub", durationType, self()),
		externMethod("sub", self(), durationType),
	)
}

// durationMethods is the operator-method signature set of the duration
// primitive. add is overloaded by argument type: another span sums, a datetime
// yields the instant the span after it. It mirrors the prelude's duration.belt.
func durationMethods() []*ir.Method {
	return append(comparisonMethods(),
		externMethod("add", self(), self()),
		externMethod("add", datetimeType, datetimeType),
		externMethod("sub", self(), self()),
		externMethod("mul", self(), intType),
	)
}

// --- intrinsics -------------------------------------------------------------

// unaryInt is a nullary-argument integer intrinsic (pos, neg).
func unaryInt(f func(a *big.Int) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 0 || r.Kind != ir.ConstInt {
			return nil
		}
		return f(r.Int)
	}
}

// binaryInt is a one-argument integer intrinsic over two integer operands.
func binaryInt(f func(a, b *big.Int) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 1 || r.Kind != ir.ConstInt || args[0].Kind != ir.ConstInt {
			return nil
		}
		return f(r.Int, args[0].Int)
	}
}

// binaryBool is a one-argument boolean intrinsic over two boolean operands.
func binaryBool(f func(a, b bool) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 1 || r.Kind != ir.ConstBool || args[0].Kind != ir.ConstBool {
			return nil
		}
		return f(r.Bool, args[0].Bool)
	}
}

// binaryStr is a one-argument string intrinsic over two string operands.
func binaryStr(f func(a, b string) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 1 || r.Kind != ir.ConstString || args[0].Kind != ir.ConstString {
			return nil
		}
		return f(r.Str, args[0].Str)
	}
}

func integerIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"pos": unaryInt(func(a *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Set(a)) }),
		"neg": unaryInt(func(a *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Neg(a)) }),
		"add": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Add(a, b)) }),
		"sub": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Sub(a, b)) }),
		"mul": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Mul(a, b)) }),
		"div": binaryInt(func(a, b *big.Int) *ir.Constant {
			if b.Sign() == 0 {
				return nil
			}
			return ir.IntConstant(new(big.Int).Quo(a, b))
		}),
		"rem": binaryInt(func(a, b *big.Int) *ir.Constant {
			if b.Sign() == 0 {
				return nil
			}
			return ir.IntConstant(new(big.Int).Rem(a, b))
		}),
		"eql":  binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) == 0) }),
		"neq":  binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) != 0) }),
		"lt":   binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) < 0) }),
		"lteq": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) <= 0) }),
		"gt":   binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) > 0) }),
		"gteq": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) >= 0) }),
	}
}

func booleanIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"not":  func(r *ir.Constant, args []*ir.Constant) *ir.Constant { return notBool(r, args) },
		"anan": binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a && b) }),
		"oror": binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a || b) }),
		"eql":  binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a == b) }),
		"neq":  binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a != b) }),
	}
}

func notBool(r *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 0 || r.Kind != ir.ConstBool {
		return nil
	}
	return ir.BoolConstant(!r.Bool)
}

// stringIntrinsics evaluates the string operators: add concatenates, and the
// comparisons use Go's lexicographic byte ordering on the operands.
func stringIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"add":  binaryStr(func(a, b string) *ir.Constant { return ir.StringConstant(a + b) }),
		"eql":  binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a == b) }),
		"neq":  binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a != b) }),
		"lt":   binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a < b) }),
		"lteq": binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a <= b) }),
		"gt":   binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a > b) }),
		"gteq": binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a >= b) }),
	}
}

// binaryMillis is a one-argument intrinsic over two millisecond-carrying
// operands of the given kinds — the building block of the datetime/duration
// operators, whose overloads differ exactly in the argument's kind.
func binaryMillis(recvKind, argKind ir.ConstKind, f func(a, b int64) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 1 || r.Kind != recvKind || args[0].Kind != argKind {
			return nil
		}
		return f(r.Millis, args[0].Millis)
	}
}

// addMillis sums two millisecond values, reporting false on int64 overflow —
// the operation then has no value, like a division by zero.
func addMillis(a, b int64) (int64, bool) {
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return 0, false
	}
	return c, true
}

// subMillis subtracts two millisecond values, reporting false on overflow.
func subMillis(a, b int64) (int64, bool) {
	c := a - b
	if (b < 0 && c < a) || (b > 0 && c > a) {
		return 0, false
	}
	return c, true
}

// checkedMillis composes an overflow-checked millisecond operation with the
// constructor of its result kind: the intrinsic body of every datetime and
// duration arithmetic overload.
func checkedMillis(op func(a, b int64) (int64, bool), build func(int64) *ir.Constant) func(a, b int64) *ir.Constant {
	return func(a, b int64) *ir.Constant {
		v, ok := op(a, b)
		if !ok {
			return nil
		}
		return build(v)
	}
}

// millisComparisons are the comparison intrinsics shared by datetime and
// duration: both order by their millisecond value.
func millisComparisons(kind ir.ConstKind) map[string]Intrinsic {
	return map[string]Intrinsic{
		"eql":  binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a == b) }),
		"neq":  binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a != b) }),
		"lt":   binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a < b) }),
		"lteq": binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a <= b) }),
		"gt":   binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a > b) }),
		"gteq": binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a >= b) }),
	}
}

// mulDuration scales a duration by an integer constant. A factor outside
// int64, or a product that overflows, has no value.
func mulDuration(r *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 || r.Kind != ir.ConstDuration || args[0].Kind != ir.ConstInt {
		return nil
	}
	if !args[0].Int.IsInt64() {
		return nil
	}
	n := args[0].Int.Int64()
	if n != 0 && (r.Millis == math.MinInt64 && n == -1 || n == math.MinInt64 && r.Millis == -1) {
		return nil // the one product the division check below cannot probe
	}
	product := r.Millis * n
	if n != 0 && product/n != r.Millis {
		return nil
	}
	return ir.DurationConstant(product)
}

// --- the standard registry --------------------------------------------------

var integerSpecs = []struct {
	name string
	kind IntKind
}{
	{"int", IntKind{Signed: true, Bits: 0}},
	{"int8", IntKind{Signed: true, Bits: 8}},
	{"int16", IntKind{Signed: true, Bits: 16}},
	{"int32", IntKind{Signed: true, Bits: 32}},
	{"int64", IntKind{Signed: true, Bits: 64}},
	{"uint", IntKind{Signed: false, Bits: 0}},
	{"uint8", IntKind{Signed: false, Bits: 8}},
	{"uint16", IntKind{Signed: false, Bits: 16}},
	{"uint32", IntKind{Signed: false, Bits: 32}},
	{"uint64", IntKind{Signed: false, Bits: 64}},
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
		r.register(spec.name, &NativeType{Name: spec.name, Int: &kind}, integerMethods(), integerIntrinsics())
	}
	r.register("bool", &NativeType{Name: "bool", Bool: true}, booleanMethods(), booleanIntrinsics())
	r.register("string", &NativeType{Name: "string", Str: true}, stringMethods(), stringIntrinsics())
	r.register("null", &NativeType{Name: "null", Null: true}, nil, nil)

	// datetime: the comparisons and the single-signature add are kind-
	// agnostic; sub is overloaded by the argument's kind — another instant
	// yields the span between them, a duration the earlier instant.
	dtI := millisComparisons(ir.ConstDatetime)
	dtI["add"] = binaryMillis(ir.ConstDatetime, ir.ConstDuration, checkedMillis(addMillis, ir.DatetimeConstant))
	r.register("datetime", &NativeType{Name: "datetime", Datetime: true}, datetimeMethods(), dtI)
	r.registerIntrinsic("datetime", "sub", []ir.ConstKind{ir.ConstDatetime},
		binaryMillis(ir.ConstDatetime, ir.ConstDatetime, checkedMillis(subMillis, ir.DurationConstant)))
	r.registerIntrinsic("datetime", "sub", []ir.ConstKind{ir.ConstDuration},
		binaryMillis(ir.ConstDatetime, ir.ConstDuration, checkedMillis(subMillis, ir.DatetimeConstant)))

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
	r.defs[name] = &ir.TypeDef{
		Name:    name,
		Public:  true,
		Body:    &ir.Builtin{Name: name},
		Methods: methods,
		Builtin: true,
	}
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
