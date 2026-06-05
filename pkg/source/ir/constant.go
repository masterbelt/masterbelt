package ir

import (
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
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
)

// Constant is the evaluated value of a constant expression: an arbitrary-
// precision integer, a boolean, a string, a collection (list/map), a datetime,
// or a duration. A nil *Constant means "could not be evaluated" — a missing
// initializer, an undefined reference, a cycle, a type error, or a division by
// zero.
type Constant struct {
	Kind   ConstKind
	Int    *big.Int     // valid when Kind == ConstInt
	Bool   bool         // valid when Kind == ConstBool
	Str    string       // valid when Kind == ConstString (the string) or ConstError (the message)
	Coll   []ConstEntry // valid when Kind == ConstCollection
	Fields []ConstField // valid when Kind == ConstRecord, in canonical (name) order
	Millis int64        // valid when Kind == ConstDatetime (UTC epoch) or ConstDuration (total)

	// valid when Kind == ConstFunc: the function literal and the values it
	// captured from its enclosing scope (the closure environment).
	Fn       *ast.FuncLit
	Captured map[string]*Constant

	// valid when Kind == ConstEnum: the enum definition and the index of the
	// member within it. Name and the base value are read from
	// EnumDef.Enum.Members[EnumIndex]; the design forbids duplicate values, so
	// the index uniquely identifies the member.
	EnumDef   *TypeDef
	EnumIndex int
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

// CollectionConstant builds a collection constant from its entries. An empty
// slice is the empty list/map; a list's entries have a nil Key.
func CollectionConstant(entries []ConstEntry) *Constant {
	return &Constant{Kind: ConstCollection, Coll: entries}
}

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

// FuncConstant builds a function-value constant from a function literal and the
// closure environment it captured (the parameter values of any enclosing
// function literals, nil at the top level).
func FuncConstant(fn *ast.FuncLit, captured map[string]*Constant) *Constant {
	return &Constant{Kind: ConstFunc, Fn: fn, Captured: captured}
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
// string, the bracketed collection ([a, b] for a list, ["k": v] for a map),
// the braced record ({ x: 1, y: 2 }, fields in canonical order), or the
// canonical datetime/duration form — the UTC instant regardless of the offset
// written, and the largest-units-first decomposition regardless of the groups
// written (90m evaluates as 1h30m).
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
