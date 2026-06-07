// This file holds the collection and range intrinsics — the foldable methods of a
// list, map, and range constant. list/map/range are not natively backed in the
// registry, so their methods (push, pop, unshift, shift, add, map, get, set, len,
// and the fold primitive every provided method is built on) have no native
// intrinsic and are folded here by name, with the fold/range walks bounded so a
// wide range never hangs the folder.
package eval

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// collectionMethod folds a method on a list/map constant. The list collections
// are not natively backed in the registry, so their methods have no intrinsic;
// the foldable ones are push/pop/unshift/shift (the stack/queue methods), add
// (the + operator), map (over a list), get (a subscript read), set (a subscript
// write), and the fold primitive. Anything else has no constant value here.
//
// A collection carries an explicit mapness (list/map/unknown), settled from its
// entries for a non-empty one and from a syntactic channel for an empty one.
// Each method is classed by whether it depends on that mapness:
//
//   - mapness-independent — fold even for an unknown empty collection, since both
//     a list and a map read the same: len/fold (count/any/all are built on it) and
//     get (a miss either way on an empty collection).
//   - mapness-dependent — does not fold for an unknown empty collection, since a
//     list and a map disagree: set (a list's out-of-range write versus a map's
//     upsert). A settled map's set is the upsert this fold's main case folds.
func collectionMethod(ctx evalCtx, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	switch name {
	case "len":
		return collectionLen(recv, args)
	case "fold":
		return collectionFold(ctx, recv, args)
	case "push":
		return collectionPush(recv, args)
	case "pop":
		return collectionPop(recv, args)
	case "unshift":
		return collectionUnshift(recv, args)
	case "shift":
		return collectionShift(recv, args)
	case "add":
		return collectionAdd(recv, args)
	case "map":
		return collectionMap(ctx, recv, args)
	case "get":
		return collectionGet(recv, args)
	case "set":
		return collectionSet(recv, args)
	default:
		return nil
	}
}

// collectionPush folds list.push(v) to a new list with the value at the end,
// leaving the receiver unchanged (data is immutable). It is the builder the
// list-returning provided methods (map, filter, keys, values) accumulate through,
// so the fold of those methods bottoms out in a real list constant. push is a
// list-only operation — a map has none — so a settled map does not fold; an
// unknown empty receiver does (pushing an element makes the result a list), and
// the result is always a list.
func collectionPush(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 || recv.IsMap() {
		return nil
	}
	out := make([]ir.ConstEntry, len(recv.Coll), len(recv.Coll)+1)
	copy(out, recv.Coll)
	out = append(out, ir.ConstEntry{Value: args[0]})
	return ir.CollectionConstantOf(out, ir.CollList)
}

// collectionUnshift folds list.unshift(v) — push's front-side mirror — to a new
// list with the value first, leaving the receiver unchanged. Like push it is
// list-only: a settled map does not fold, an unknown empty receiver does, and
// the result is always a list.
func collectionUnshift(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 || recv.IsMap() {
		return nil
	}
	out := make([]ir.ConstEntry, 0, len(recv.Coll)+1)
	out = append(out, ir.ConstEntry{Value: args[0]})
	out = append(out, recv.Coll...)
	return ir.CollectionConstantOf(out, ir.CollList)
}

// collectionPop folds list.pop() to the last element, or to null when the list
// is empty — the taking read is a value to branch on (optional<T>), never an
// error, and the receiver stays unchanged. pop is list-only, so a settled map
// does not fold; an unknown empty receiver does (only a list type-checks a pop,
// and an empty read is null regardless of the element type).
func collectionPop(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 0 || recv.IsMap() {
		return nil
	}
	if len(recv.Coll) == 0 {
		return ir.NullConstant()
	}
	return recv.Coll[len(recv.Coll)-1].Value
}

// collectionShift folds list.shift() — pop's front-side mirror — to the first
// element, or to null when the list is empty, the receiver unchanged. Like pop
// it is list-only: a settled map does not fold, an unknown empty receiver does.
func collectionShift(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 0 || recv.IsMap() {
		return nil
	}
	if len(recv.Coll) == 0 {
		return ir.NullConstant()
	}
	return recv.Coll[0].Value
}

// collectionAdd folds the + operator on a list — the one overloaded collection
// method: add(other: self) concatenates two lists, add(element: T) pushes the
// one element. The folder is value-blind, so it re-decides the overload from
// the operand's shape the way SelectOverload decided it from the types: the
// argument is read as another list of the receiver's elements (self) and as one
// element of the receiver (T), and the call folds only when exactly one reading
// fits — an undecidable or ambiguous operand (an empty receiver with a list
// argument, say) leaves the call unevaluated rather than guessing between the
// two. A settled map's + (its add(other: self)) is not folded here.
func collectionAdd(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 || recv.IsMap() {
		return nil
	}
	arg := args[0]
	asSelf := arg.Kind == ir.ConstCollection && !arg.IsMap() && entriesFitElements(recv, arg.Coll)
	asElement := elementFits(recv, arg)
	if asSelf == asElement {
		return nil // neither or both readings fit: do not guess
	}
	if asElement {
		return collectionPush(recv, args)
	}
	out := make([]ir.ConstEntry, 0, len(recv.Coll)+len(arg.Coll))
	out = append(out, recv.Coll...)
	out = append(out, arg.Coll...)
	return ir.CollectionConstantOf(out, ir.CollList)
}

// elementFits reports whether v could be one element of the receiver, judged by
// value alone: it has the shape of the receiver's elements. An empty receiver
// pins no element shape, so anything fits it.
func elementFits(recv *ir.Constant, v *ir.Constant) bool {
	if len(recv.Coll) == 0 {
		return true
	}
	return sameShape(recv.Coll[0].Value, v)
}

// entriesFitElements reports whether every entry could be an element of the
// receiver — i.e. the entries read as another list of the receiver's element
// type: all unkeyed, each fitting the receiver's element shape. An empty slice
// pins nothing and fits.
func entriesFitElements(recv *ir.Constant, entries []ir.ConstEntry) bool {
	for _, e := range entries {
		if e.Key != nil || !elementFits(recv, e.Value) {
			return false
		}
	}
	return true
}

// sameShape reports whether two constants could inhabit the same type, judged
// by value alone: their kinds agree, and two collections agree on mapness and
// on element shape — recursively, by their first entries, since typing keeps a
// folded collection homogeneous. An empty collection pins no element shape and
// matches any collection whose mapness does not contradict it. The check backs
// collectionAdd's overload re-decision, so it errs undecidable-as-fitting and
// lets the exactly-one-fits rule reject the ambiguity.
func sameShape(a, b *ir.Constant) bool {
	if a == nil || b == nil || a.Kind != b.Kind {
		return false
	}
	if a.Kind != ir.ConstCollection {
		return true
	}
	if (a.IsMap() && b.IsList()) || (a.IsList() && b.IsMap()) {
		return false
	}
	if len(a.Coll) == 0 || len(b.Coll) == 0 {
		return true // an empty side pins no element shape
	}
	if (a.Coll[0].Key != nil) != (b.Coll[0].Key != nil) {
		return false
	}
	return sameShape(a.Coll[0].Value, b.Coll[0].Value)
}

// collectionLen folds list.len() and map.len() to the element/entry count. It is
// the intrinsic E-18 supplied for neither list nor map; the count is the same
// for both — the number of entries the folded collection carries.
func collectionLen(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 0 {
		return nil
	}
	return ir.IntConstant(big.NewInt(int64(len(recv.Coll))))
}

// collectionFold folds the native fold — the foldable primitive every provided
// method (count, any, all, map, filter, keys, values) is built on. It threads an
// accumulator from init through the step function, visiting every entry in fold
// order: the step sees (acc, key, value), where a map's key is the entry's key
// and a list's is the element index. An unfoldable step application (a non-
// function step, a body that does not fold, or the recursion guard) leaves the
// whole fold unevaluated.
func collectionFold(ctx evalCtx, recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 2 || args[1].Kind != ir.ConstFunc {
		return nil
	}
	acc := args[0]
	step := args[1]
	for i, entry := range recv.Coll {
		key := entry.Key
		if key == nil {
			key = ir.IntConstant(big.NewInt(int64(i))) // a list's key is the index
		}
		acc = apply(ctx, step, []*ir.Constant{acc, key, entry.Value})
		if acc == nil {
			return nil
		}
	}
	return acc
}

// rangeMethod folds a method on a range constant. range is not natively backed
// in the registry, so its native methods are folded here by name: the foldable
// primitive fold — the same model list/map follow, where the provided methods
// (count, any, all, map, filter, keys, values) reach the body through the
// foldable impl and bottom out in this fold — and the comparable operators
// eql/neq, which fold by the range's identity (its start, end, and step). A
// range's equality does fold to a constant (unlike a list's, whose elements are
// not known to the type rule), so (0..9) == range(0, 9) folds true. Anything
// else has no constant value here.
func rangeMethod(ctx evalCtx, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	switch name {
	case "fold":
		return rangeFold(ctx, recv, args)
	case "eql", "neq":
		if len(args) != 1 || args[0].Kind != ir.ConstRange {
			return nil
		}
		equal := ir.ConstantsEqual(recv, args[0])
		if name == "neq" {
			equal = !equal
		}
		return ir.BoolConstant(equal)
	default:
		return nil
	}
}

// rangeCount returns the number of elements a range visits — the count of the
// sequence start, start+step, ..., staying on the end side of step — and whether
// the bounds are present to compute it. The formula is (end - start) / step + 1
// when the bounds run in the step's direction (the quotient floors toward zero,
// which is exact here because start and end are aligned to start + k*step only
// when the remainder vanishes; a partial last stride still lands inside the
// bound), clamped at zero when end is past start against the step's sign. It is
// O(1): a wide or descending range is counted from its bounds without a walk, so
// the maxRangeIterations cap is decided before any iteration.
func rangeCount(recv *ir.Constant) (*big.Int, bool) {
	if recv == nil || recv.Start == nil || recv.End == nil {
		return nil, false
	}
	step := recv.RangeStep()
	span := new(big.Int).Sub(recv.End, recv.Start)
	// The span must run in the step's direction, or the range is empty: an
	// ascending step (step > 0) needs end >= start (span >= 0), a descending one
	// (step < 0) needs end <= start (span <= 0). A zero span (start == end) is one
	// element either way.
	if span.Sign() != 0 && span.Sign() != step.Sign() {
		return big.NewInt(0), true
	}
	count := new(big.Int).Quo(span, step) // floors toward zero; span and step share a sign
	count.Add(count, big.NewInt(1))
	return count, true
}

// rangeElement returns the i-th element of a range (0-based): start + i*step.
func rangeElement(recv *ir.Constant, i int64) *big.Int {
	v := new(big.Int).Mul(recv.RangeStep(), big.NewInt(i))
	return v.Add(v, recv.Start)
}

// rangeFold folds range.fold — the foldable primitive every provided method is
// built on. It threads an accumulator over the sequence start, start+step, ...,
// the step function seeing (acc, key, value) where the key is the element's
// 0-based position (a range's key is its index, like a list's) and the value is
// the element. An empty range (end past start against the step's sign) folds to
// the initial accumulator. The walk is bounded by maxRangeIterations: a range
// wider than the cap does not fold (nil), so a wide range never hangs the folder
// or exhausts memory. An unfoldable step application (a non-function step, a body
// that does not fold, or the recursion guard) also leaves the fold unevaluated.
func rangeFold(ctx evalCtx, recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 2 || args[1].Kind != ir.ConstFunc {
		return nil
	}
	// The element count is computed from the bounds and step in O(1). A count past
	// the cap does not fold — checked on the big.Int before any iteration, so a
	// wide range is rejected without being walked.
	count, ok := rangeCount(recv)
	if !ok {
		return nil
	}
	if count.Sign() <= 0 {
		return args[0] // the empty range folds to the initial accumulator
	}
	if count.Cmp(big.NewInt(maxRangeIterations)) > 0 {
		return nil // wider than the compile-time iteration bound: do not fold
	}
	acc := args[0]
	step := args[1]
	n := count.Int64()
	for i := int64(0); i < n; i++ {
		key := ir.IntConstant(big.NewInt(i))           // the 0-based position
		value := ir.IntConstant(rangeElement(recv, i)) // the element
		acc = apply(ctx, step, []*ir.Constant{acc, key, value})
		if acc == nil {
			return nil
		}
	}
	return acc
}

// collectionMap folds list.map: it applies the function argument to each element
// and collects the results into a new list. A settled map (keyed entries) reaches
// its provided foldable.map through the def channel instead, so a keyed entry here
// does not fold; an unknown empty receiver folds to the empty list — map over no
// elements is the empty list whichever kind it is. The result is always a list.
func collectionMap(ctx evalCtx, recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 || args[0].Kind != ir.ConstFunc {
		return nil
	}
	out := make([]ir.ConstEntry, len(recv.Coll))
	for i, entry := range recv.Coll {
		if entry.Key != nil {
			return nil // map.map (keyed entries) is not foldable
		}
		v := apply(ctx, args[0], []*ir.Constant{entry.Value})
		if v == nil {
			return nil
		}
		out[i] = ir.ConstEntry{Value: v}
	}
	return ir.CollectionConstantOf(out, ir.CollList)
}

// collectionGet folds a subscript read coll.get(i). A read can miss — a list
// index out of range, a map key not present — and a miss is a value, an error
// constant, not an unfoldable result: the read folds to that error so a caller
// can branch on it. get is mapness-independent: an empty collection has no element
// whichever kind it is, so the read always misses and folds — an empty map (or a
// non-integer index, which only a map accepts) misses by key, anything else by
// index.
func collectionGet(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 {
		return nil
	}
	key := args[0]
	if recv.IsMap() {
		for _, entry := range recv.Coll {
			if entry.Key != nil && constEqual(entry.Key, key) {
				return entry.Value
			}
		}
		return ir.ErrorConstant("key not found")
	}
	i, ok := intIndex(key)
	if !ok {
		// A non-integer index reaches here only on an empty unknown collection (a
		// settled list rejects it as a type error, a map took the branch above): with
		// no element to read, the miss is a key-not-found, the same error a map gives.
		if len(recv.Coll) == 0 {
			return ir.ErrorConstant("key not found")
		}
		return nil // a non-integer index on a list is a type error the checker reports
	}
	if i < 0 || i >= int64(len(recv.Coll)) {
		return ir.ErrorConstant("index out of range")
	}
	return recv.Coll[int(i)].Value
}

// collectionSet folds a subscript write coll.set(i, v) to the new collection it
// returns, leaving the receiver unchanged (data is immutable). set is the one
// mapness-dependent write: a map's set is an upsert (an existing key's value is
// replaced, a new key appended — it always succeeds, so an empty map's set folds
// to the single-entry map, the main case this whole change enables), while a
// list's replaces the element at an in-range index (an out-of-range index does not
// fold, the compile-time write past the end the semantic layer reports as
// index_out_of_range). An unknown empty collection — whose mapness no channel
// settled — does not fold (nil) rather than guess between the two; the result
// keeps the receiver's mapness.
func collectionSet(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 2 {
		return nil
	}
	value := args[1]
	if recv.IsMap() {
		key := args[0]
		out := make([]ir.ConstEntry, len(recv.Coll))
		replaced := false
		for i, entry := range recv.Coll {
			if entry.Key != nil && constEqual(entry.Key, key) {
				out[i] = ir.ConstEntry{Key: entry.Key, Value: value}
				replaced = true
				continue
			}
			out[i] = entry
		}
		if !replaced {
			out = append(out, ir.ConstEntry{Key: key, Value: value})
		}
		return ir.CollectionConstantOf(out, ir.CollMap)
	}
	if !recv.IsList() {
		return nil // an unknown empty collection: do not guess list-versus-map
	}
	i, ok := intIndex(args[0])
	if !ok {
		return nil
	}
	if i < 0 || i >= int64(len(recv.Coll)) {
		return nil // out of range: a compile-time error, reported as index_out_of_range
	}
	out := make([]ir.ConstEntry, len(recv.Coll))
	copy(out, recv.Coll)
	out[int(i)] = ir.ConstEntry{Value: value}
	return ir.CollectionConstantOf(out, ir.CollList)
}

// intIndex reads an integer constant as a list index, reporting whether it is an
// integer that fits an int64. A negative or oversized index is out of range,
// which the caller turns into a miss (for a read) or an unfoldable write — both
// compared against the collection's length as an int64.
func intIndex(c *ir.Constant) (int64, bool) {
	if c == nil || c.Kind != ir.ConstInt || !c.Int.IsInt64() {
		return 0, false
	}
	return c.Int.Int64(), true
}
