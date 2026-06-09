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

// fieldNames renders a master's row fields as "name:type" pairs in source order.
func fieldNames(def *ir.TypeDef) string {
	parts := make([]string, len(def.Master.Fields))
	for i, f := range def.Master.Fields {
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
