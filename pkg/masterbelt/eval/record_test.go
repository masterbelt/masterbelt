package eval

import (
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// pointDef builds a record type def `type Point = { lv: Level }` whose lv field
// is the given nominal type.
func pointDef(fieldName string, fieldType *ir.TypeDef) *ir.TypeDef {
	return &ir.TypeDef{
		Name: "Point",
		Body: &ir.Record{Fields: []ir.Field{{Name: fieldName, Type: &ir.Named{Def: fieldType}}}},
	}
}

// memberField builds the field access recv.name.
func memberField(recv ast.Expr, name string) *ast.MemberExpr {
	return ast.NewMemberExpr(recv, ast.NewIdentifier(name, nil), nil)
}

// recordVal builds a record constant with one field.
func recordVal(name string, value *ir.Constant) *ir.Constant {
	return ir.RecordConstant([]ir.ConstField{{Name: name, Value: value}})
}

// TestRecordFieldReceiverFold covers the record-field receiver channel: a const
// of a record type whose field is a nominal type folds a method call on the
// field (p.lv.increment()), reading the field's static type from the record def.
func TestRecordFieldReceiverFold(t *testing.T) {
	level := levelDef()
	point := pointDef("lv", level)
	env := newTypeEnv(level, point).withConst("p", "Point", recordVal("lv", intConst(5)))
	// p.lv.increment() — the receiver p.lv is a Level (the record's field type).
	wantInt(t, memberCall(memberField(id("p"), "lv"), "increment"), env, 6)
}

// TestRecordFieldReceiverChainFold covers a method chain off a record field:
// p.lv.increment().increment() resolves the result def from the self-returning
// method, the way a non-field chain does.
func TestRecordFieldReceiverChainFold(t *testing.T) {
	level := levelDef()
	point := pointDef("lv", level)
	env := newTypeEnv(level, point).withConst("p", "Point", recordVal("lv", intConst(5)))
	chain := memberCall(memberCall(memberField(id("p"), "lv"), "increment"), "increment")
	wantInt(t, chain, env, 7)
}

// TestRecordFieldUnknownFieldSafe covers a field name the record does not have:
// the receiver def does not resolve, so the method call does not fold (nil) —
// the conservative failure, never a wrong fold.
func TestRecordFieldUnknownFieldSafe(t *testing.T) {
	level := levelDef()
	point := pointDef("lv", level)
	env := newTypeEnv(level, point).withConst("p", "Point", recordVal("lv", intConst(5)))
	// p.missing.increment() — no such field; the value read fails and the method
	// has no receiver def, so it does not fold.
	wantNil(t, memberCall(memberField(id("p"), "missing"), "increment"), env)
}

// TestRecordFieldNonRecordBaseSafe covers a base that is not a record (a plain
// int const): a field access on it resolves no record, so a method on the
// "field" does not fold.
func TestRecordFieldNonRecordBaseSafe(t *testing.T) {
	level := levelDef()
	env := newTypeEnv(level).withConst("n", "int", intConst(5))
	// n.lv.increment() — n is an int, not a record; nothing folds.
	wantNil(t, memberCall(memberField(id("n"), "lv"), "increment"), env)
}

// TestRecordFieldLiteralBaseSafe covers a record literal receiver with no
// annotation channel: the field's value still reads (the record value carries
// its fields), but the field's static type is unknown — a literal names no type
// — so a user method on it does not fold, the conservative failure.
func TestRecordFieldLiteralBaseSafe(t *testing.T) {
	rec := ast.NewRecordLit("Point", []*ast.FieldInit{ast.NewFieldInit("lv", intLit("5"), nil)}, nil)
	env := newTypeEnv(levelDef())
	wantNil(t, memberCall(memberField(rec, "lv"), "increment"), env)
}
