package ir

import (
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CollKind is the three-valued list/map distinction (the "mapness") of a folded
// collection constant. A non-empty literal settles it by syntax — a keyed entry
// makes it a map, a bare element a list — but an empty literal carries no key to
// read, so its mapness comes from a syntactic channel (a const/let/param type
// annotation, a callee's declared result type) when there is one, and stays
// CollUnknown when there is not. The mapness decides the one operation the two
// kinds disagree on (set: a list's out-of-range write versus a map's upsert) and
// the meaning of the key in keys/values/map/filter/for; the operations that read
// the same for both (len, fold, get, any, all) ignore it.
type CollKind int

const (
	CollUnknown CollKind = iota // an empty literal with no settling channel
	CollList                    // a list (bare elements, or a list-typed channel)
	CollMap                     // a map (keyed entries, or a map-typed channel)
)

// ConstKind distinguishes the kinds of evaluated constant value.
type ConstKind int

const (
	ConstInt        ConstKind = iota // an arbitrary-precision integer (Constant.Int)
	ConstBool                        // a boolean (Constant.Bool)
	ConstString                      // a string (Constant.Str)
	ConstCollection                  // a list or map (Constant.Coll)
	ConstFunc                        // a function value (Constant.Fn / Constant.Captured)
	ConstDatetime                    // a UTC instant in epoch milliseconds (Constant.Millis)
	ConstDuration                    // a span of time in milliseconds (Constant.Millis)
	ConstRecord                      // a record value (Constant.Fields)
	ConstError                       // an error value carrying its message (Constant.Str)
	ConstEnum                        // an enum member value (Constant.EnumDef / Constant.EnumIndex)
	ConstNull                        // the null value (no payload — the single inhabitant of the null type)
	ConstRange                       // an inclusive integer range (Constant.Start / Constant.End)
)

// Constant is the evaluated value of a constant expression: an arbitrary-
// precision integer, a boolean, a string, a collection (list/map), a datetime,
// a duration, or an inclusive integer range. A nil *Constant means "could not be
// evaluated" — a missing initializer, an undefined reference, a cycle, a type
// error, or a division by zero.
type Constant struct {
	Kind   ConstKind
	Int    *big.Int     // valid when Kind == ConstInt
	Bool   bool         // valid when Kind == ConstBool
	Str    string       // valid when Kind == ConstString (the string) or ConstError (the message)
	Coll   []ConstEntry // valid when Kind == ConstCollection
	Fields []ConstField // valid when Kind == ConstRecord, in canonical (name) order

	// CollMapness is the list/map distinction of a collection constant (valid
	// when Kind == ConstCollection). A non-empty collection always has a settled
	// mapness from its entries; an empty one is CollUnknown unless a syntactic
	// channel settled it. See CollKind.
	CollMapness CollKind
	Millis      int64 // valid when Kind == ConstDatetime (UTC epoch) or ConstDuration (total)

	// valid when Kind == ConstFunc: the function-literal value and the values
	// it captured from its enclosing scope (the closure environment). Fn is
	// the IR node — the folded value references no syntax (F-3 §2.4); the
	// literal's surface form stays reachable through Fn.Syntax, the
	// transitional channel the AST-driven application reads until the folder
	// interprets the IR body directly (F-3 M5).
	Fn       *FuncLiteral
	Captured map[string]*Constant

	// valid when Kind == ConstEnum: the enum definition and the index of the
	// member within it. Name and the base value are read from
	// EnumDef.Enum.Members[EnumIndex]; the design forbids duplicate values, so
	// the index uniquely identifies the member.
	EnumDef   *TypeDef
	EnumIndex int

	// valid when Kind == ConstRange: the integer sequence Start, Start+Step, ...,
	// staying on the End side of Step — for a positive Step the elements up to and
	// including End (Start, Start+Step, ..., <= End), for a negative Step down to
	// and including End (>= End). Step is never zero on a folded range; a nil Step
	// reads as 1 (the two-argument range's step), so a range built without one — by
	// RangeConstant or an older caller — behaves exactly as before. An End past the
	// first element in the step's direction is the empty range. The bounds and step
	// are kept lazily (the sequence is never materialized here), so a wide range is
	// a small value — the evaluator bounds the walk it makes over one.
	Start *big.Int
	End   *big.Int
	Step  *big.Int // nil reads as 1

	// UnionTag is the union member a value flowed in as — the tag a tagged union
	// carries. It is nil when the value never passed through a union expectation
	// (a plain integer, a bare record), and the member *type itself* (a
	// Named{Coin}, a Builtin{error}) — never the union — when it did, so the tag
	// stays valid as the value moves between a bare union (Coin | Level) and a
	// nominal alias (GameValue) of it. The tag is what lets a match dispatch a
	// value whose kind several members back (a record union Coin | Level, two
	// nominal-over-int Small | Big): the runtime arm is the one whose member type
	// equals the tag. It is set on a copy of the value at the tagging sites (a
	// const/let/param/return/field/argument union channel) and read back by the
	// match folder and equality; an untagged value is unchanged, so values that
	// never meet a union are exactly as before.
	//
	// Tagging is the execution of the IR's explicit union-inflow adaption: the
	// Adapt node the post-check write-back wraps a union channel in (F-3 §2.2)
	// records the same member selection (types.SelectUnionMember) the folder's
	// expectation-driven tagging computes — UnionTag is what evaluating that
	// Adapt produces.
	UnionTag Type
}

// ConstantsEqual reports whether two folded constants are structurally equal —
// the single equality the evaluator's map-key/switch matching and the semantic
// engine's early cutoff both dispatch on. Two nil constants (an unevaluated
// value on both sides) are equal; a nil against a non-nil is not. Differing
// kinds are never equal. The scalar kinds compare by value, an error by its
// message, an enum member by identity (definition pointer and member index), a
// datetime/duration by its milliseconds, a range by its bounds, and the
// composite kinds recursively: a collection by length and entrywise key/value
// equality, a record by its canonical (name-sorted) fields. A function value
// compares by the identity of its literal (the AST pointer) and the equality of
// its captured environment —
// a re-parsed but textually identical literal is a different fact, so it is not
// equal to the original.
//
// Every ConstKind is handled explicitly; a kind added later that is not listed
// here makes this function panic rather than silently report "not equal", so
// the new kind forces an update of both call sites it serves.
func ConstantsEqual(a, b *Constant) bool {
	if a == b {
		return true // identical pointers, including both nil
	}
	if a == nil || b == nil || a.Kind != b.Kind {
		return false
	}
	// The union tag is part of a value's identity: a Coin and a Level with the
	// same fields are different members, and a tagged value is not the same fact
	// as the same payload untagged (a later channel could tag it differently). A
	// tag present on one side but not the other is unequal — the safe side for the
	// engine's early cutoff, which recomputes rather than coalescing two facts a
	// member tag could tell apart.
	if !tagsEqual(a.UnionTag, b.UnionTag) {
		return false
	}
	switch a.Kind {
	case ConstInt:
		return a.Int != nil && b.Int != nil && a.Int.Cmp(b.Int) == 0
	case ConstBool:
		return a.Bool == b.Bool
	case ConstString:
		return a.Str == b.Str
	case ConstError:
		return a.Str == b.Str
	case ConstEnum:
		return a.EnumDef == b.EnumDef && a.EnumIndex == b.EnumIndex
	case ConstNull:
		// null has a single inhabitant: two null values (same kind, already
		// checked above) are always equal.
		return true
	case ConstDatetime, ConstDuration:
		return a.Millis == b.Millis
	case ConstRange:
		// Two ranges are equal when their bounds and step are equal — a range
		// carries no other identity. The step compares through RangeStep, so a nil
		// step (the unit-step range) equals an explicit step of 1; range(0, 9) and
		// range(0, 9, 1) are the same value. An empty range still compares by its
		// written bounds (range(10, 10) is not range(5, 5)), which is conservative
		// and matches the String() rendering; nothing relies on every empty range
		// being one value.
		return a.Start != nil && b.Start != nil && a.End != nil && b.End != nil &&
			a.Start.Cmp(b.Start) == 0 && a.End.Cmp(b.End) == 0 &&
			a.RangeStep().Cmp(b.RangeStep()) == 0
	case ConstCollection:
		// The mapness is part of a collection's identity: an empty list is not an
		// empty map (their set/keys/iteration meanings differ), and an empty
		// CollUnknown collection — whose mapness a channel has not settled — is
		// equal only to another CollUnknown, never to a settled empty list or map.
		// Treating Unknown-versus-settled as unequal is the safe side for the
		// engine's early cutoff: it triggers a recompute rather than coalescing two
		// facts a later channel could tell apart.
		if a.CollMapness != b.CollMapness {
			return false
		}
		if len(a.Coll) != len(b.Coll) {
			return false
		}
		for i := range a.Coll {
			// A list entry has a nil key on both sides; a map entry's keys
			// must match too. A key present on one side but not the other (a
			// list against a map of the same length) is unequal.
			if (a.Coll[i].Key == nil) != (b.Coll[i].Key == nil) {
				return false
			}
			if a.Coll[i].Key != nil && !ConstantsEqual(a.Coll[i].Key, b.Coll[i].Key) {
				return false
			}
			if !ConstantsEqual(a.Coll[i].Value, b.Coll[i].Value) {
				return false
			}
		}
		return true
	case ConstRecord:
		// RecordConstant normalizes fields to canonical name order, so equal
		// records have identically ordered fields: a positional walk suffices.
		if len(a.Fields) != len(b.Fields) {
			return false
		}
		for i := range a.Fields {
			if a.Fields[i].Name != b.Fields[i].Name || !ConstantsEqual(a.Fields[i].Value, b.Fields[i].Value) {
				return false
			}
		}
		return true
	case ConstFunc:
		// Two function values are the same exactly when they close over the
		// same literal: the literal's syntax identifies it (the AST pointer is
		// the engine's fact, surviving the fold rebuilding the IR node), with
		// the node pointer the fallback for a literal built without syntax.
		if funcIdentity(a.Fn) != funcIdentity(b.Fn) || len(a.Captured) != len(b.Captured) {
			return false
		}
		for name, v := range a.Captured {
			w, ok := b.Captured[name]
			if !ok || !ConstantsEqual(v, w) {
				return false
			}
		}
		return true
	default:
		panic("ir.ConstantsEqual: unhandled ConstKind " + strconv.Itoa(int(a.Kind)))
	}
}

// tagsEqual reports whether two union tags denote the same member. A tag is the
// member type a value flowed in as, which is always a nominal type (Named — a
// record, refinement, or enum member) or a primitive (Builtin — error, nint),
// so member identity is the Named's definition pointer or the Builtin's name —
// the same two-form identity the tagging sites and the match folder read. Two nil
// tags (neither value carries one) are equal; a tag on one side only is not.
func tagsEqual(a, b Type) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch a := a.(type) {
	case *Named:
		b, ok := b.(*Named)
		return ok && a.Def == b.Def
	case *Builtin:
		b, ok := b.(*Builtin)
		return ok && a.Name == b.Name
	default:
		// Any other type form is not a member a value flows in as, so two such
		// tags are equal only when identical pointers — the conservative default.
		return a == b
	}
}

// Tagged returns a copy of the constant carrying the union member tag, or the
// constant unchanged when tag is nil (the value did not flow through a union) or
// the constant is nil (an unfoldable value takes no tag). The copy is shallow —
// it shares the payload — so tagging a value never mutates the original a caller
// still holds; the tag is the one field that differs. Re-tagging with the member
// a value already carries is the identity, so a value moving through a chain of
// union channels of the same member keeps one stable tag.
func Tagged(c *Constant, tag Type) *Constant {
	if c == nil || tag == nil {
		return c
	}
	tagged := *c
	tagged.UnionTag = tag
	return &tagged
}

// Untagged returns a copy of the constant with its union tag cleared, or the
// constant unchanged when it carries no tag (or is nil). It is how a match arm
// narrows a value to its member type: inside the arm the value is the member, not
// the union, so its tag is dropped and the payload reads as the bare member type.
func Untagged(c *Constant) *Constant {
	if c == nil || c.UnionTag == nil {
		return c
	}
	bare := *c
	bare.UnionTag = nil
	return &bare
}

// ConstEntry is one entry of a folded collection constant: a Value, and for a
// map entry a Key (nil for a list element).
type ConstEntry struct {
	Key   *Constant // nil for a list element
	Value *Constant
}

// ConstField is one field of a folded record constant: its name and value.
type ConstField struct {
	Name  string
	Value *Constant
}

// IntConstant builds an integer constant.
func IntConstant(n *big.Int) *Constant { return &Constant{Kind: ConstInt, Int: n} }

// BoolConstant builds a boolean constant.
func BoolConstant(b bool) *Constant { return &Constant{Kind: ConstBool, Bool: b} }

// StringConstant builds a string constant.
func StringConstant(s string) *Constant { return &Constant{Kind: ConstString, Str: s} }

// ErrorConstant builds an error constant from its message.
func ErrorConstant(message string) *Constant { return &Constant{Kind: ConstError, Str: message} }

// NullConstant builds the null value — the single inhabitant of the null type,
// carrying no payload.
func NullConstant() *Constant { return &Constant{Kind: ConstNull} }

// RangeConstant builds the unit-step integer range [start, end]: the elements
// start, start+1, ..., end, with an end below start being the empty
// range. The step is left nil (reads as 1), so this is the two-argument range's
// value, byte-identical to its old representation. The bounds are held lazily;
// the sequence is materialized only when a fold or a for walks it, under the
// evaluator's iteration bound.
func RangeConstant(start, end *big.Int) *Constant {
	return &Constant{Kind: ConstRange, Start: start, End: end}
}

// RangeConstantStep builds the stepped integer range start, start+step, ...,
// staying on the end side of step. A nil or unit step builds the same value as
// RangeConstant; a non-unit step is carried so the walk and the count honour it.
// The caller guarantees a non-zero step (the type/eval layers reject step 0 and
// fold a zero-step range to nothing); this constructor does not re-check.
func RangeConstantStep(start, end, step *big.Int) *Constant {
	if step != nil && step.Cmp(bigOne) == 0 {
		step = nil // the unit step is the canonical nil, so range(0, 9, 1) == range(0, 9)
	}
	return &Constant{Kind: ConstRange, Start: start, End: end, Step: step}
}

// bigOne is the constant 1, the value a nil range Step reads as.
var bigOne = big.NewInt(1)

// RangeStep returns the range's step, reading a nil Step as 1 (the two-argument
// range's step). It is the one place the nil-reads-as-1 convention is decoded, so
// every walk, count, and comparison over a range agrees on the step.
func (c *Constant) RangeStep() *big.Int {
	if c == nil || c.Step == nil {
		return bigOne
	}
	return c.Step
}

// CollectionConstant builds a collection constant from its entries, settling its
// mapness from them: a keyed entry makes it a map, a bare element a list, and an
// empty slice — which carries no key to read — is the conservative CollUnknown.
// Use CollectionConstantOf to supply a mapness for an empty literal a syntactic
// channel settled, or to carry the receiver's mapness through an operation.
func CollectionConstant(entries []ConstEntry) *Constant {
	return CollectionConstantOf(entries, collMapnessOf(entries))
}

// CollectionConstantOf builds a collection constant with an explicit mapness —
// the form an empty literal a type channel settled (an empty map<K,V> annotation)
// and an operation that preserves the receiver's mapness (set/map/filter over an
// empty collection) build through. A non-empty collection's entries already settle
// its mapness, so kind must agree with them; the caller passes the same mapness
// collMapnessOf would derive.
func CollectionConstantOf(entries []ConstEntry, kind CollKind) *Constant {
	return &Constant{Kind: ConstCollection, Coll: entries, CollMapness: kind}
}

// collMapnessOf derives the mapness a slice of entries settles by syntax: a keyed
// entry makes it a map, a bare element a list, and an empty slice is CollUnknown.
func collMapnessOf(entries []ConstEntry) CollKind {
	for _, e := range entries {
		if e.Key != nil {
			return CollMap
		}
	}
	if len(entries) == 0 {
		return CollUnknown
	}
	return CollList
}

// IsMap reports whether a collection constant is a map (CollMap) — true only when
// its mapness is settled to map, so an empty CollUnknown collection is not a map.
func (c *Constant) IsMap() bool { return c != nil && c.CollMapness == CollMap }

// IsList reports whether a collection constant is a list (CollList) — true only
// when its mapness is settled to list, so an empty CollUnknown collection is not
// a list.
func (c *Constant) IsList() bool { return c != nil && c.CollMapness == CollList }

// RecordConstant builds a record constant from its fields, normalizing to the
// canonical order — sorted by field name, a duplicate name keeping the last
// value — so two records with the same fields render identically regardless of
// the order the literal wrote them in.
func RecordConstant(fields []ConstField) *Constant {
	canon := make([]ConstField, 0, len(fields))
	index := make(map[string]int, len(fields))
	for _, f := range fields {
		if i, ok := index[f.Name]; ok {
			canon[i] = f // a duplicate initializer: the last value wins
			continue
		}
		index[f.Name] = len(canon)
		canon = append(canon, f)
	}
	sort.Slice(canon, func(i, j int) bool { return canon[i].Name < canon[j].Name })
	return &Constant{Kind: ConstRecord, Fields: canon}
}

// FuncConstant builds a function-value constant from a function-literal value
// and the closure environment it captured (the parameter values of any
// enclosing function literals, nil at the top level).
func FuncConstant(fn *FuncLiteral, captured map[string]*Constant) *Constant {
	return &Constant{Kind: ConstFunc, Fn: fn, Captured: captured}
}

// funcIdentity is the identity a function value compares by: the literal's
// syntax pointer when it has one, else the IR node itself. See ConstantsEqual's
// ConstFunc case.
func funcIdentity(fn *FuncLiteral) any {
	if fn == nil {
		return nil
	}
	if fn.Syntax != nil {
		return fn.Syntax
	}
	return fn
}

// EnumConstant builds an enum member value from its definition and the member's
// index within it.
func EnumConstant(def *TypeDef, index int) *Constant {
	return &Constant{Kind: ConstEnum, EnumDef: def, EnumIndex: index}
}

// EnumName returns the name of the member an enum constant denotes, or "" when
// the constant is not an enum or its index is out of range.
func (c *Constant) EnumName() string {
	if c == nil || c.Kind != ConstEnum || c.EnumDef == nil || c.EnumDef.Enum == nil {
		return ""
	}
	if c.EnumIndex < 0 || c.EnumIndex >= len(c.EnumDef.Enum.Members) {
		return ""
	}
	return c.EnumDef.Enum.Members[c.EnumIndex].Name
}

// EnumValue returns the base-type value of the member an enum constant denotes,
// or nil when it is unavailable.
func (c *Constant) EnumValue() *Constant {
	if c == nil || c.Kind != ConstEnum || c.EnumDef == nil || c.EnumDef.Enum == nil {
		return nil
	}
	if c.EnumIndex < 0 || c.EnumIndex >= len(c.EnumDef.Enum.Members) {
		return nil
	}
	return c.EnumDef.Enum.Members[c.EnumIndex].Value
}

// DatetimeConstant builds a datetime constant from a UTC instant in epoch
// milliseconds.
func DatetimeConstant(millis int64) *Constant {
	return &Constant{Kind: ConstDatetime, Millis: millis}
}

// DurationConstant builds a duration constant from a total in milliseconds.
func DurationConstant(millis int64) *Constant {
	return &Constant{Kind: ConstDuration, Millis: millis}
}

// String renders the constant's value: the integer, "true"/"false", the quoted
// string, the bracketed collection ([a, b] for a list, ["k": v] for a map), the
// braced record ({ x: 1, y: 2 }, fields in canonical order), or the canonical
// datetime/duration form — the UTC instant regardless of the offset written, and
// the largest-units-first decomposition regardless of the groups written (90m
// evaluates as 1h30m). An empty list and an empty CollUnknown both render []; an
// empty map renders [:] — a diagnostic-only marker (the language has no [:]
// literal) so a folded empty map is told apart from an empty list in a dump.
func (c *Constant) String() string {
	if c == nil {
		return "<unevaluated>"
	}
	switch c.Kind {
	case ConstBool:
		return strconv.FormatBool(c.Bool)
	case ConstString:
		return strconv.Quote(c.Str)
	case ConstFunc:
		return "<func>"
	case ConstCollection:
		if len(c.Coll) == 0 && c.CollMapness == CollMap {
			return "[:]" // an empty map: a marker, since the language has no [:] literal
		}
		parts := make([]string, len(c.Coll))
		for i, e := range c.Coll {
			if e.Key != nil {
				parts[i] = e.Key.String() + ": " + e.Value.String()
			} else {
				parts[i] = e.Value.String()
			}
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case ConstRecord:
		if len(c.Fields) == 0 {
			return "{}"
		}
		parts := make([]string, len(c.Fields))
		for i, f := range c.Fields {
			parts[i] = f.Name + ": " + f.Value.String()
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case ConstError:
		return "error(" + strconv.Quote(c.Str) + ")"
	case ConstNull:
		return "null"
	case ConstRange:
		// The two-argument form for a unit step (range(0, 9)), the three-argument
		// form when a step is carried (range(9, 0, -1)) — the very spelling the
		// equivalent constructor takes, so a literal and its range(...) read alike.
		if c.Step == nil {
			return "range(" + c.Start.String() + ", " + c.End.String() + ")"
		}
		return "range(" + c.Start.String() + ", " + c.End.String() + ", " + c.Step.String() + ")"
	case ConstEnum:
		name := c.EnumName()
		if c.EnumDef == nil {
			return name
		}
		return c.EnumDef.Name + "." + name
	case ConstDatetime:
		return "D" + time.UnixMilli(c.Millis).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	case ConstDuration:
		return formatDuration(c.Millis)
	default:
		return c.Int.String()
	}
}

// durationUnits is the canonical decomposition order of a duration, largest
// first: weeks, days, hours, minutes, seconds, milliseconds.
var durationUnits = [...]struct {
	suffix string
	millis int64
}{
	{"w", 7 * 24 * 60 * 60 * 1000},
	{"d", 24 * 60 * 60 * 1000},
	{"h", 60 * 60 * 1000},
	{"m", 60 * 1000},
	{"s", 1000},
	{"ms", 1},
}

// formatDuration renders a duration's total milliseconds in canonical form:
// largest units first, zero components omitted, the zero duration as 0ms, and
// a computed negative span (an earlier instant minus a later one) signed. The
// magnitude decomposes as a uint64 because the most negative int64 has no
// int64 negation — two's complement gives its exact magnitude instead.
func formatDuration(millis int64) string {
	if millis == 0 {
		return "0ms"
	}
	var b strings.Builder
	mag := uint64(millis)
	if millis < 0 {
		b.WriteString("-")
		mag = -mag
	}
	for _, u := range durationUnits {
		if n := mag / uint64(u.millis); n > 0 {
			b.WriteString(strconv.FormatUint(n, 10))
			b.WriteString(u.suffix)
			mag -= n * uint64(u.millis)
		}
	}
	return b.String()
}
