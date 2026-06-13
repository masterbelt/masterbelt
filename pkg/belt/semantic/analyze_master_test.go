// This file pins the master-declaration semantics: a master resolves to an
// opaque nominal TypeDef carrying its row fields and primary key (Body stays
// nil), its row methods read their fields through self, a primary key naming no
// field is reported, and a master shares the type name space so a name a
// type/enum/interface claims collides.
package semantic

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// masterDef returns the resolved master definition of the given name, or nil.
func masterDef(m *ir.Module, name string) *ir.TypeDef {
	for _, t := range m.Types {
		if t.Name == name && t.Master != nil {
			return t
		}
	}
	return nil
}

// fieldNames renders a master's row fields as "name:type" pairs in source order,
// reading them from the row type the descriptor keeps.
func fieldNames(def *ir.TypeDef) string {
	rec := underlyingRecord(def.Master.Row)
	if rec == nil {
		return ""
	}
	parts := make([]string, len(rec.Fields))
	for i, f := range rec.Fields {
		parts[i] = f.Name + ":" + f.Type.String()
	}
	return strings.Join(parts, ",")
}

// TestMasterResolvesToOpaqueNominal pins the happy path: a master's record field
// types resolve (a field typed by an enum becomes a Named of it), its primary
// key is recorded, and it is an opaque nominal — Body stays nil, so the type
// algebra treats it as a leaf. A row method reading self.field type-checks, so
// the whole declaration is diagnostic-free.
func TestMasterResolvesToOpaqueNominal(t *testing.T) {
	src := "enum SkillKind {\n  active\n  passive\n}\n\n" +
		"master Skill {\n" +
		"  record {\n    id: int,\n    name: string,\n    kind: SkillKind,\n  } impl {\n" +
		"    pub label(): string {\n      return self.name\n    }\n  }\n" +
		"  primary id\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	def := masterDef(m, "Skill")
	if def == nil {
		t.Fatalf("master Skill not resolved: %+v", m.Types)
	}
	if def.Body != nil {
		t.Errorf("master Body = %v, want nil (opaque nominal)", def.Body)
	}
	if got, want := fieldNames(def), "id:int,name:string,kind:SkillKind"; got != want {
		t.Errorf("fields = %q, want %q", got, want)
	}
	if got, want := len(def.Master.Primary), 1; want != got || def.Master.Primary[0] != "id" {
		t.Errorf("primary = %v, want [id]", def.Master.Primary)
	}
	if def.Anchor != "belt:/Skill" {
		t.Errorf("anchor = %q, want belt:/Skill", def.Anchor)
	}
}

// TestMasterCompositePrimary pins a multi-column primary key: every column is a
// field, in declaration order.
func TestMasterCompositePrimary(t *testing.T) {
	src := "master SkillUpgrade {\n  record {\n    skill: int,\n    level: int,\n  }\n  primary (skill, level)\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	def := masterDef(m, "SkillUpgrade")
	if def == nil {
		t.Fatalf("master SkillUpgrade not resolved")
	}
	if got := def.Master.Primary; len(got) != 2 || got[0] != "skill" || got[1] != "level" {
		t.Errorf("primary = %v, want [skill level]", got)
	}
}

// TestMasterPrimaryUnknownField pins the only new diagnostic: a primary key that
// names no row field is reported, once, as master_primary_unknown_field — and a
// composite key reports each unknown column.
func TestMasterPrimaryUnknownField(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int // number of master_primary_unknown_field diagnostics expected
	}{
		{
			name: "single unknown",
			src:  "master M {\n  record {\n    id: int,\n  }\n  primary nope\n}\n",
			want: 1,
		},
		{
			name: "one of composite unknown",
			src:  "master M {\n  record {\n    a: int,\n    b: int,\n  }\n  primary (a, missing)\n}\n",
			want: 1,
		},
		{
			name: "both composite unknown",
			src:  "master M {\n  record {\n    a: int,\n  }\n  primary (x, y)\n}\n",
			want: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			got := 0
			for _, d := range diags {
				if d.Code == CodeMasterPrimaryUnknownField {
					got++
				}
			}
			if got != tc.want {
				t.Fatalf("master_primary_unknown_field count = %d, want %d (all: %v)", got, tc.want, codes(diags))
			}
		})
	}
}

// TestMasterRowNamedRecordType pins that a master row written as a named record
// type resolves its fields from the alias (record Row, type Row = { ... }), so
// the primary key is found and the master is diagnostic-free — the row resolver
// unwraps a nominal alias rather than only accepting an inline record.
func TestMasterRowNamedRecordType(t *testing.T) {
	src := "type Row = { id: int, name: string }\n" +
		"master M {\n  record Row\n  primary id\n}\n"
	m, diags := analyze(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes(diags))
	}
	def := masterDef(m, "M")
	if def == nil {
		t.Fatalf("master M not resolved")
	}
	if got, want := fieldNames(def), "id:int,name:string"; got != want {
		t.Errorf("fields = %q, want %q", got, want)
	}
}

// TestMasterMissingPrimary pins that a master with no primary key is rejected: a
// master with no key cannot identify a row, so an omitted primary member is an
// error, not silently accepted.
func TestMasterMissingPrimary(t *testing.T) {
	src := "master M {\n  record {\n    id: int,\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMasterMissingPrimary) {
		t.Fatalf("want master_missing_primary, got %v", codes(diags))
	}
}

// TestMasterMissingRow pins that a master with no row record is rejected: the
// parser leaves an absent record member to the semantic layer, so a master with
// no row (record absent or not a record type) is reported, not published as an
// unusable empty-row descriptor. With no row the key-existence check is skipped,
// so a primary column does not also report as unknown.
func TestMasterMissingRow(t *testing.T) {
	src := "master M {\n  primary id\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMasterMissingRow) {
		t.Fatalf("want master_missing_row, got %v", codes(diags))
	}
	if hasCode(diags, CodeMasterPrimaryUnknownField) {
		t.Errorf("did not expect a primary-unknown diagnostic when the row is missing: %v", codes(diags))
	}
}

// TestMasterRowMustBeRecord pins that a row that is genuinely not a record (a
// builtin, an enum) is reported as a missing row, distinct from the deferred
// generic-alias case below.
func TestMasterRowMustBeRecord(t *testing.T) {
	src := "master M {\n  record int\n  primary id\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMasterMissingRow) {
		t.Fatalf("want master_missing_row for a non-record row, got %v", codes(diags))
	}
}

// TestMasterGenericRowAliasDeferred pins that a generic record alias row is a
// real record this slice does not expand: it is not falsely reported as a
// missing row, and its (unknown) key column is not flagged unknown. Full support
// for generic row aliases is left to later work.
func TestMasterGenericRowAliasDeferred(t *testing.T) {
	src := "type Row<T> = { id: T }\nmaster M {\n  record Row<int>\n  primary id\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeMasterMissingRow) {
		t.Errorf("a generic record alias row should not be reported missing: %v", codes(diags))
	}
	if hasCode(diags, CodeMasterPrimaryUnknownField) {
		t.Errorf("a deferred generic alias row should not flag its key unknown: %v", codes(diags))
	}
}

// TestMasterDuplicateKeyOnDeferredRow pins that a repeated primary column is
// reported even when the row is a deferred generic alias whose fields are not
// expanded — the duplicate check needs no field list, only the unknown-column
// check does.
func TestMasterDuplicateKeyOnDeferredRow(t *testing.T) {
	src := "type Row<T> = { id: T }\nmaster M {\n  record Row<int>\n  primary (id, id)\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMasterDuplicatePrimaryKey) {
		t.Fatalf("want master_duplicate_primary_key on a deferred row, got %v", codes(diags))
	}
	if hasCode(diags, CodeMasterMissingRow) {
		t.Errorf("a generic alias row should not be reported missing: %v", codes(diags))
	}
}

// TestMasterDuplicatePrimaryKey pins that a composite primary key repeating a
// column is rejected: a key tuple must not name a column twice, or a consumer
// building key tuples or foreign-key references would see it doubled.
func TestMasterDuplicatePrimaryKey(t *testing.T) {
	src := "master M {\n  record {\n    id: int,\n  }\n  primary (id, id)\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMasterDuplicatePrimaryKey) {
		t.Fatalf("want master_duplicate_primary_key, got %v", codes(diags))
	}
}

// TestMasterValidateRejectsNonAssert pins that a validate each block holds only
// assert statements: a let (or any non-assert) is reported rather than carried
// and silently dropped from the per-row fold, where its local would leave the
// assertion unfoldable and fail every row.
func TestMasterValidateRejectsNonAssert(t *testing.T) {
	src := "master M {\n  record { id: int }\n  primary id\n  validate {\n    each {\n      let x = 1\n      assert self.id > x\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMasterValidateNotAssert) {
		t.Fatalf("want master_validate_not_assert, got %v", codes(diags))
	}
}

// TestMasterValidateAcceptsAsserts pins the clean case: a validate each block of
// only assert statements resolves without the not-assert diagnostic.
func TestMasterValidateAcceptsAsserts(t *testing.T) {
	src := "master M {\n  record { id: int, cost: int }\n  primary id\n  validate {\n    each {\n      assert self.id > 0\n      assert self.cost >= 0\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeMasterValidateNotAssert) {
		t.Errorf("an assert-only validate block should be accepted: %v", codes(diags))
	}
}

// TestMasterValidateCyclicRowDoesNotCrash pins that a validate check on a master
// whose row type is a cyclic alias does not send the foldability probe chasing
// the cycle: the ill-formed row is already reported, and building a witness for
// it is skipped rather than overflowing the stack.
func TestMasterValidateCyclicRowDoesNotCrash(t *testing.T) {
	src := "type A = B\ntype B = A\nmaster M {\n  record { id: A }\n  primary id\n  validate {\n    each {\n      assert self.id > 0\n    }\n  }\n}\n"
	// Returning at all means the foldability probe terminated rather than chasing
	// the cycle into a stack overflow; the ill-formed row is reported.
	_, diags := analyze(src)
	if len(diags) == 0 {
		t.Fatal("want the ill-formed cyclic row reported, got none")
	}
}

// TestMasterValidateOverloadFolds pins that the foldability probe runs after the
// write-back binds overload selections: a recursive ok(long) declared before a
// foldable ok(int), with the int overload selected for ok(self.id) on an int
// row, must fold the selected overload — not the first same-arity one — so the
// check is not falsely reported non-constant.
func TestMasterValidateOverloadFolds(t *testing.T) {
	src := "fn ok(x: long): bool {\n  return ok(x)\n}\nfn ok(x: int): bool {\n  return x > 0\n}\nmaster M {\n  record { id: int }\n  primary id\n  validate {\n    each {\n      assert ok(self.id)\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeMasterValidateNotConstant) {
		t.Errorf("the selected ok(int) overload folds, so the check is constant: %v", codes(diags))
	}
}

// TestMasterValidateAliasedRowMethod pins that a row method folds when the row is
// reached through an alias chain (record Row, type Row = Base): the evaluator
// unwraps the row recursively to find the record the master backs, so self.ok()
// folds rather than reporting the check non-constant.
func TestMasterValidateAliasedRowMethod(t *testing.T) {
	src := "type Base = { id: int }\ntype Row = Base\nmaster M {\n  record Row impl {\n    ok(): bool { return self.id > 0 }\n  }\n  primary id\n  validate {\n    each {\n      assert self.ok()\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if hasCode(diags, CodeMasterValidateNotConstant) {
		t.Errorf("a row method on an aliased row folds: %v", codes(diags))
	}
}

// TestMasterValidateRejectsUnfoldable pins that a validate check that cannot
// fold to a bool — here an unbounded recursion the interpreter cannot settle — is
// reported once at compile time, rather than folding to nil and failing every
// loaded row as a row-validation error.
func TestMasterValidateRejectsUnfoldable(t *testing.T) {
	src := "fn loop(x: int): bool {\n  return loop(x)\n}\nmaster M {\n  record { id: int }\n  primary id\n  validate {\n    each {\n      assert loop(self.id)\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMasterValidateNotConstant) {
		t.Fatalf("want master_validate_not_constant for an unfoldable check, got %v", codes(diags))
	}
}

// TestMasterValidateRejectsEffect pins that a validate check is pure: an
// effectful call in one is reported as a missing effect (validate has nowhere to
// declare one), rather than silently failing every row at fold time.
func TestMasterValidateRejectsEffect(t *testing.T) {
	src := "extern fn nondet roll(): int\nmaster M {\n  record { id: int }\n  primary id\n  validate {\n    each {\n      assert self.id > roll()\n    }\n  }\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMissingEffect) {
		t.Fatalf("want missing_effect for an effectful validate check, got %v", codes(diags))
	}
}

// TestEnclosingDeclMaster pins that an offset inside a master maps to the
// master's stable anchor — the anchor lookup reads the master backpointer like
// it does a type/enum/interface, so a diagnostic anchored in a master resolves.
func TestEnclosingDeclMaster(t *testing.T) {
	src := "pub master Foo {\n  record {\n    id: int\n  }\n  primary id\n}\n"
	p := buildProgram(map[string]string{"game.belt": src})
	assertClean(t, p, "game.belt")
	offset := strings.Index(src, "record")
	got, ok := p.EnclosingDecl("game.belt", offset+1)
	if !ok || got != "belt:game/Foo" {
		t.Errorf("EnclosingDecl inside master = %q (ok=%v), want belt:game/Foo", got, ok)
	}
}

// TestMasterOpaqueToRecordLiteral pins the opaque-nominal rule: a record literal
// cannot target a master type even though the master's row has those fields. The
// row shape is readable through a receiver (self.name) but the master is not
// assignable to or from its row record, so this is a type error.
func TestMasterOpaqueToRecordLiteral(t *testing.T) {
	src := "master Skill {\n  record {\n    id: int,\n    name: string,\n  }\n  primary id\n}\n" +
		"const S: Skill = { id: 1, name: \"x\" }\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeTypeMismatch) {
		t.Fatalf("want type_mismatch (a master is opaque to its row record), got %v", codes(diags))
	}
}

// TestMasterBuiltinSurface pins that a master impl is held to the builtin-surface
// rule like a type's: an extern method or a `= builtin` constant in a user file
// is reported, not silently admitted.
func TestMasterBuiltinSurface(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{
			name: "extern method",
			src:  "master M {\n  record {\n    id: int,\n  } impl {\n    pub extern fn native(): int\n  }\n  primary id\n}\n",
			want: CodeExternOutsideBuiltin,
		},
		{
			name: "builtin const",
			src:  "master M {\n  record {\n    id: int,\n  } impl {\n    const Max = builtin\n  }\n  primary id\n}\n",
			want: CodeBuiltinOutsideBuiltin,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if !hasCode(diags, tc.want) {
				t.Fatalf("want %s, got %v", tc.want, codes(diags))
			}
		})
	}
}

// TestMasterAccessorCollidesWithRowField pins that a getter/setter named like a
// row field collides, just as it does on a type. A master keeps Body nil and
// stores its row fields on the descriptor, so the member check must read them
// from there — otherwise the collision goes unreported.
func TestMasterAccessorCollidesWithRowField(t *testing.T) {
	src := "master M {\n" +
		"  record {\n    name: string,\n  } impl {\n" +
		"    pub get name(): string {\n      return \"\"\n    }\n  }\n" +
		"  primary name\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeAccessorCollision) {
		t.Fatalf("want accessor_collision, got %v", codes(diags))
	}
}

// TestMasterImplConstChecked pins that a master's impl associated constants get
// the same initializer diagnostics a type's or enum's do: an undefined name, a
// stray self, and an effect in the compile-time (pure) context. Without the
// master path joining those checks these would silently pass.
func TestMasterImplConstChecked(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want diagnostic.Code
	}{
		{
			name: "undefined name",
			src:  "master M {\n  record {\n    id: int,\n  } impl {\n    const X: int = Nope\n  }\n  primary id\n}\n",
			want: CodeUndefinedName,
		},
		{
			name: "stray self",
			src:  "master M {\n  record {\n    id: int,\n  } impl {\n    const X: int = self.id\n  }\n  primary id\n}\n",
			want: CodeSelfOutsideMethod,
		},
		{
			name: "effect in pure context",
			src: "pub type C = { d: nint } impl {\n  pub static fn io load(): C {\n    return C{ d: 0 }\n  }\n}\n" +
				"master M {\n  record {\n    id: int,\n  } impl {\n    const X = C.load()\n  }\n  primary id\n}\n",
			want: CodeEffectInPureContext,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if !hasCode(diags, tc.want) {
				t.Fatalf("want %s, got %v", tc.want, codes(diags))
			}
		})
	}
}

// TestMasterDuplicatePrimaryKeyNotPersisted pins that a repeated primary column
// is both reported and dropped from the IR, so a downstream consumer reading the
// recovered module never builds a doubled key tuple.
func TestMasterDuplicatePrimaryKeyNotPersisted(t *testing.T) {
	src := "master M {\n  record {\n    id: int,\n  }\n  primary (id, id)\n}\n"
	m, diags := analyze(src)
	if !hasCode(diags, CodeMasterDuplicatePrimaryKey) {
		t.Fatalf("want master_duplicate_primary_key, got %v", codes(diags))
	}
	def := masterDef(m, "M")
	if def == nil {
		t.Fatal("master M not resolved")
	}
	if got := def.Master.Primary; len(got) != 1 || got[0] != "id" {
		t.Errorf("Master.Primary = %v, want the de-duplicated [id]", got)
	}
}

// TestMasterWhereUnsupported pins that a row predicate (where) on a master is
// rejected as not-yet-supported rather than silently dropped — row validation is
// later work.
func TestMasterWhereUnsupported(t *testing.T) {
	src := "master M {\n  record {\n    id: int,\n  } where true\n  primary id\n}\n"
	_, diags := analyze(src)
	if !hasCode(diags, CodeMasterWhereUnsupported) {
		t.Fatalf("want master_where_unsupported, got %v", codes(diags))
	}
}

// TestMasterSharesTypeNamespace pins that a master shares the one type name
// space, so a name a type, enum, or interface already claims is a redeclaration,
// reported by the existing duplicate_declaration path (no master machinery of
// its own).
func TestMasterSharesTypeNamespace(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"type then master", "type Skill = int\nmaster Skill {\n  record {\n    id: int,\n  }\n  primary id\n}\n"},
		{"master then enum", "master Skill {\n  record {\n    id: int,\n  }\n  primary id\n}\nenum Skill {\n  a\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := analyze(tc.src)
			if !hasCode(diags, CodeDuplicateDeclaration) {
				t.Fatalf("want duplicate_declaration, got %v", codes(diags))
			}
		})
	}
}
