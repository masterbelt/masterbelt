package load_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/semantic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/master"
	"github.com/masterbelt/masterbelt/pkg/master/format/csv"
	"github.com/masterbelt/masterbelt/pkg/master/load"
)

// skillBelt is a master with a refined field; the source's base path is supplied
// by the caller, not the manifest, so the loader is exercised without a project.
const skillBelt = "type Level = int where self > 0\n\n" +
	"master Skill {\n" +
	"  record { id: int, name: string, power: Level }\n" +
	"  primary id\n" +
	"  source { csv \"skills.csv\" }\n" +
	"}\n"

// run analyzes beltSrc as one file, writes each data file under a temp root, and
// loads the master data with csv registered and the given per-format base paths.
func run(t *testing.T, beltSrc string, bases map[string]string, files map[string]string) ([]load.Loaded, []diagnostic.Diagnostic) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prog := semantic.NewProgram()
	prog.SetFile("skills.belt", abstract.NewDocument([]byte(beltSrc)), nil)
	prog.Refresh()
	reg := master.NewRegistry()
	reg.Register(csv.New())
	return load.File(prog, "skills.belt", root, bases, reg)
}

func TestLoadTypedRows(t *testing.T) {
	loaded, diags := run(t, skillBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,name,power\n1,Fireball,30\n2,Heal,12\n",
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded = %d tables, want 1", len(loaded))
	}
	got := loaded[0]
	if got.Master != "Skill" || got.Display != "data/skills.csv" {
		t.Errorf("loaded = %+v, want Skill <- data/skills.csv", got)
	}
	if s := got.Table.String(); s != "id, name, power\n1 | \"Fireball\" | 30\n2 | \"Heal\" | 12\n" {
		t.Errorf("table =\n%q", s)
	}
}

func TestLoadCellTypeMismatch(t *testing.T) {
	_, diags := run(t, skillBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,name,power\noops,Heal,12\n",
	})
	d := single(t, diags, master.CodeCellTypeMismatch)
	for _, frag := range []string{"data/skills.csv:2,1", "oops", "int", "id"} {
		if !strings.Contains(d.Message, frag) {
			t.Errorf("message = %q, want %q", d.Message, frag)
		}
	}
}

func TestLoadRefinementViolation(t *testing.T) {
	_, diags := run(t, skillBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,name,power\n1,Drain,-5\n",
	})
	d := single(t, diags, master.CodeCellRefinement)
	for _, frag := range []string{"data/skills.csv:2,9", "-5", "Level"} {
		if !strings.Contains(d.Message, frag) {
			t.Errorf("message = %q, want %q", d.Message, frag)
		}
	}
}

func TestLoadMissingColumn(t *testing.T) {
	_, diags := run(t, skillBelt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,name\n1,Heal\n",
	})
	d := single(t, diags, master.CodeMissingColumn)
	if !strings.Contains(d.Message, "power") {
		t.Errorf("message = %q, want it to name the missing field", d.Message)
	}
}

func TestLoadBasePathResolves(t *testing.T) {
	// The locator resolves under the base path the caller gives, nowhere else.
	belt := "master Skill {\n  record { id: int, name: string }\n  primary id\n  source { csv \"skills.csv\" }\n}\n"
	loaded, diags := run(t, belt, map[string]string{"csv": "assets/master"}, map[string]string{
		"assets/master/skills.csv": "id,name\n1,Heal\n",
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
	if loaded[0].Display != "assets/master/skills.csv" {
		t.Errorf("display = %q, want the base-path-resolved name", loaded[0].Display)
	}
}

func TestLoadLocatorEscapesRoot(t *testing.T) {
	// A locator climbing past the root with `..` is refused, not read.
	belt := "master Skill {\n  record { id: int }\n  primary id\n  source { csv \"../../secret.csv\" }\n}\n"
	loaded, diags := run(t, belt, map[string]string{"csv": "data"}, nil)
	d := single(t, diags, master.CodeLocatorEscapesRoot)
	if !strings.Contains(d.Message, "secret.csv") {
		t.Errorf("message = %q, want it to name the locator", d.Message)
	}
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing read for an escaping locator", loaded)
	}
}

func TestLoadSkipsCoercionOnReadError(t *testing.T) {
	// The csv is absent: the read fails, and the failure is reported on its own
	// — not buried under a missing-column error for every field, nor an empty
	// table printed as if it had loaded.
	loaded, diags := run(t, skillBelt, map[string]string{"csv": "data"}, nil)
	single(t, diags, master.CodeSourceUnreadable)
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing on a failed read", loaded)
	}
}

func TestLoadFollowsAliasChain(t *testing.T) {
	// Level is a plain alias of the refined Positive; the predicate must still
	// run through the alias.
	belt := "type Positive = int where self > 0\n" +
		"type Level = Positive\n\n" +
		"master Skill {\n" +
		"  record { id: int, power: Level }\n" +
		"  primary id\n" +
		"  source { csv \"skills.csv\" }\n" +
		"}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{
		"data/skills.csv": "id,power\n1,-5\n",
	})
	d := single(t, diags, master.CodeCellRefinement)
	if !strings.Contains(d.Message, "-5") {
		t.Errorf("message = %q, want the violating value", d.Message)
	}
}

func TestLoadGenericRowUnsupported(t *testing.T) {
	// A generic row alias is a shape the reader does not expand; rather than
	// silently skipping the master's sources, it is reported as unsupported.
	belt := "type Row<T> = { id: T }\n\n" +
		"master M {\n  record Row<int>\n  primary id\n  source { csv \"m.csv\" }\n}\n"
	loaded, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{"data/m.csv": "id\n1\n"})
	single(t, diags, master.CodeUnsupportedRowType)
	if len(loaded) != 0 {
		t.Errorf("loaded = %v, want nothing for an unsupported row", loaded)
	}
}

func TestLoadGenericFieldUnsupported(t *testing.T) {
	// A generic-alias field type is reported as unsupported rather than silently
	// dropped; the concrete fields around it still bind.
	belt := "type Id<T> = T\n\n" +
		"master Skill {\n  record { id: int, val: Id<int> }\n  primary id\n  source { csv \"skills.csv\" }\n}\n"
	_, diags := run(t, belt, map[string]string{"csv": "data"}, map[string]string{"data/skills.csv": "id,val\n1,2\n"})
	d := single(t, diags, master.CodeUnsupportedFieldType)
	if !strings.Contains(d.Message, "val") {
		t.Errorf("message = %q, want it to name the generic field", d.Message)
	}
}

func TestLoadUnknownFormat(t *testing.T) {
	belt := "master Skill {\n  record { id: int }\n  primary id\n  source { xlsx \"skills.xlsx\" }\n}\n"
	_, diags := run(t, belt, nil, nil)
	d := single(t, diags, master.CodeUnknownFormat)
	if !strings.Contains(d.Message, "xlsx") {
		t.Errorf("message = %q, want it to name the unknown format", d.Message)
	}
}

func TestLoadBadOptions(t *testing.T) {
	for _, c := range []struct {
		name, opts string
		code       diagnostic.Code
	}{
		{"unknown key", "{ sheet: \"S1\" }", master.CodeUnknownOption},
		{"wrong type", "{ delimiter: 5 }", master.CodeOptionTypeMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			belt := "master Skill {\n  record { id: int }\n  primary id\n  source { csv \"skills.csv\" " + c.opts + " }\n}\n"
			_, diags := run(t, belt, nil, map[string]string{"skills.csv": "id\n1\n"})
			single(t, diags, c.code)
		})
	}
}

// single asserts diags holds exactly one diagnostic of the given code.
func single(t *testing.T, diags []diagnostic.Diagnostic, code diagnostic.Code) diagnostic.Diagnostic {
	t.Helper()
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one %s", diags, code)
	}
	if diags[0].Code != code {
		t.Fatalf("code = %s, want %s", diags[0].Code, code)
	}
	return diags[0]
}
