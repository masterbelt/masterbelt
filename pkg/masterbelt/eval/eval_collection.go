// This file holds the collection and range intrinsics — the foldable methods of a
// list, map, and range constant. list/map/range are not natively backed in the
// registry, so their methods (append, map, get, set, len, and the fold primitive
// every provided method is built on) have no native intrinsic and are folded
// here by name, with the fold/range walks bounded so a wide range never hangs the
// folder.
package eval

import (
	"math/big"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// collectionMethod folds a method on a list/map constant. The list collections
// are not natively backed in the registry, so their methods have no intrinsic;
// the foldable ones are append, map (over a list), get (a subscript read), set (a
// subscript write), and the fold primitive. Anything else has no constant value
// here.
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
	case "append":
		return collectionAppend(recv, args)
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

// collectionAppend folds list.append(v) to a new list with the value at the end,
// leaving the receiver unchanged (data is immutable). It is the builder the
// list-returning provided methods (map, filter, keys, values) accumulate through,
// so the fold of those methods bottoms out in a real list constant. append is a
// list-only operation — a map has none — so a settled map does not fold; an
// unknown empty receiver does (appending an element makes the result a list), and
// the result is always a list.
func collectionAppend(recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 || recv.IsMap() {
		return nil
	}
	out := make([]ir.ConstEntry, len(recv.Coll), len(recv.Coll)+1)
	copy(out, recv.Coll)
	out = append(out, ir.ConstEntry{Value: args[0]})
	return ir.CollectionConstantOf(out, ir.CollList)
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
// in the registry, so its only native method is the foldable primitive fold —
// the same model list/map follow, where the provided methods (count, any, all,
// map, filter, keys, values) reach the body through the foldable impl and bottom
// out in this fold. Anything else has no constant value here.
func rangeMethod(ctx evalCtx, recv *ir.Constant, name string, args []*ir.Constant) *ir.Constant {
	if name == "fold" {
		return rangeFold(ctx, recv, args)
	}
	return nil
}

// rangeFold folds range.fold — the foldable primitive every provided method is
// built on. It threads an accumulator over the half-open sequence start..end-1,
// the step seeing (acc, key, value) where the key is the element's 0-based
// position (a range's key is its index, like a list's) and the value is the
// element. An end at or below start is the empty range, which folds to the
// initial accumulator. The walk is bounded by maxRangeIterations: a range wider
// than the cap does not fold (nil), so a wide range never hangs the folder or
// exhausts memory. An unfoldable step application (a non-function step, a body
// that does not fold, or the recursion guard) also leaves the fold unevaluated.
func rangeFold(ctx evalCtx, recv *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 2 || args[1].Kind != ir.ConstFunc {
		return nil
	}
	if recv.Start == nil || recv.End == nil {
		return nil
	}
	// The element count is end - start, clamped at zero. A count past the cap does
	// not fold — checked on the big.Int before any iteration, so a wide range is
	// rejected in O(1) rather than walked.
	count := new(big.Int).Sub(recv.End, recv.Start)
	if count.Sign() <= 0 {
		return args[0] // the empty range folds to the initial accumulator
	}
	if count.Cmp(big.NewInt(maxRangeIterations)) > 0 {
		return nil // wider than the compile-time iteration bound: do not fold
	}
	acc := args[0]
	step := args[1]
	cur := new(big.Int).Set(recv.Start)
	one := big.NewInt(1)
	for i := int64(0); cur.Cmp(recv.End) < 0; i++ {
		key := ir.IntConstant(big.NewInt(i))           // the 0-based position
		value := ir.IntConstant(new(big.Int).Set(cur)) // the element
		acc = apply(ctx, step, []*ir.Constant{acc, key, value})
		if acc == nil {
			return nil
		}
		cur.Add(cur, one)
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
