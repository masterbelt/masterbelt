package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/internal/belttest"
)

// execCheck executes `masterbelt check args...` through the root command and
// returns its combined output.
func execCheck(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	RootCmd.SetOut(&out)
	RootCmd.SetErr(&out)
	RootCmd.SetArgs(append([]string{"check"}, args...))
	t.Cleanup(func() {
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
		RootCmd.SetArgs([]string{})
		// Flag values persist on the command between Execute calls.
		_ = RootCmd.PersistentFlags().Set("reporter", reporterText)
		_ = CheckCmd.Flags().Set("locale", "en")
		_ = CheckCmd.Flags().Set("profile", "")
	})
	err := RootCmd.Execute()
	return out.String(), err
}

// write creates path under root (and any parent directories) with content.
func TestCheckProject(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"src/main.belt\"\n")
	belttest.WriteFile(t, root, "src/main.belt", "const MaxLevel: long = 100\n")

	out, err := execCheck(t, root)
	if err != nil {
		t.Fatalf("check = %v\n%s", err, out)
	}
	if out != "" {
		t.Errorf("output = %q, want none", out)
	}
}

func TestCheckProjectFromSubdirectory(t *testing.T) {
	// With no argument, check starts the manifest search at the current
	// directory, go.mod style.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")
	belttest.WriteFile(t, root, "sub/keep", "")
	t.Chdir(filepath.Join(root, "sub"))

	if out, err := execCheck(t); err != nil {
		t.Fatalf("check = %v\n%s", err, out)
	}
}

func TestCheckReportsSourceErrors(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = B\n")

	out, err := execCheck(t, root)
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	// The diagnostic is anchored to the entry file with line and column.
	if !strings.Contains(out, "main.belt:1:11: error[belt.semantic.undefined_name]: undefined name: B") {
		t.Errorf("output = %q, want the anchored undefined_name diagnostic", out)
	}
}

func TestCheckReportsManifestErrors(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "name = \"broken\"\n")

	out, err := execCheck(t, root)
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	if !strings.Contains(out, "masterbelt.toml:1:1: error[project.config.missing_entry]") {
		t.Errorf("output = %q, want the missing_entry diagnostic anchored to the manifest", out)
	}
}

func TestCheckWithoutProject(t *testing.T) {
	// Note: this would find a masterbelt.toml above the temp dir if one
	// existed there; temp dirs live outside any project.
	out, err := execCheck(t, t.TempDir())
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	if !strings.Contains(out, "error[project.config.missing]") {
		t.Errorf("output = %q, want the missing-manifest diagnostic", out)
	}
}

func TestCheckMultiFileProject(t *testing.T) {
	// The whole import closure analyzes as one program: a clean cross-file
	// reference passes, and an error inside an imported file is reported at
	// that file's own path.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "use geo from \"geometry.belt\"\nconst start = geo.Origin\n")
	belttest.WriteFile(t, root, "geometry.belt", "pub const Origin = 0\n")

	if out, err := execCheck(t, root); err != nil {
		t.Fatalf("check = %v\n%s", err, out)
	}

	belttest.WriteFile(t, root, "geometry.belt", "pub const Origin = Broken\n")
	out, err := execCheck(t, root)
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	if !strings.Contains(out, "geometry.belt:1:20: error[belt.semantic.undefined_name]: undefined name: Broken") {
		t.Errorf("output = %q, want the error anchored in geometry.belt", out)
	}
}

func TestCheckUseNotFound(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "use ghost from \"missing.belt\"\nconst a = 1\n")

	out, err := execCheck(t, root)
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	if !strings.Contains(out, "error[belt.semantic.use_not_found]: imported file not found: missing.belt") {
		t.Errorf("output = %q, want the use_not_found diagnostic", out)
	}
}

func TestCheckJSON(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = B\n")

	out, err := execCheck(t, "--reporter=json", root)
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}

	var report struct {
		Version     int `json:"version"`
		Diagnostics []struct {
			Code   string `json:"code"`
			File   string `json:"file"`
			Anchor string `json:"anchor"`
			Range  *struct {
				Start struct {
					Offset, Line, Column int
				} `json:"start"`
			} `json:"range"`
			Message struct {
				Locale, Text string
			} `json:"message"`
			Data  map[string]string `json:"data"`
			Fixes []any             `json:"fixes"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if report.Version != 1 || len(report.Diagnostics) != 1 {
		t.Fatalf("report = %+v, want version 1 with one diagnostic", report)
	}
	d := report.Diagnostics[0]
	if d.Code != "belt.semantic.undefined_name" {
		t.Errorf("code = %q", d.Code)
	}
	if !strings.HasSuffix(d.File, "main.belt") {
		t.Errorf("file = %q, want the entry file", d.File)
	}
	// The undefined reference sits inside `const A`, so the diagnostic carries
	// that declaration's stable anchor.
	if d.Anchor != "belt:main/A" {
		t.Errorf("anchor = %q, want belt:main/A", d.Anchor)
	}
	if d.Range == nil || d.Range.Start.Line != 1 || d.Range.Start.Column != 11 || d.Range.Start.Offset != 10 {
		t.Errorf("range = %+v, want 1:11 at offset 10", d.Range)
	}
	// Machines read code + data instead of parsing the message.
	if d.Data["name"] != "B" {
		t.Errorf("data = %v, want name=B", d.Data)
	}
	if d.Fixes == nil {
		t.Errorf("fixes is null, want an (empty) array")
	}
}

func TestCheckJSONClean(t *testing.T) {
	// A clean run still emits a well-formed document, and exits zero.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

	out, err := execCheck(t, "--reporter=json", root)
	if err != nil {
		t.Fatalf("check = %v\n%s", err, out)
	}
	var report struct {
		Diagnostics []any `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if report.Diagnostics == nil || len(report.Diagnostics) != 0 {
		t.Errorf("diagnostics = %v, want an empty array", report.Diagnostics)
	}
}

func TestCheckJSONManifestError(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "name = \"broken\"\n")

	out, err := execCheck(t, "--reporter=json", root)
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	for _, fragment := range []string{`"code": "project.config.missing_entry"`, `masterbelt.toml"`} {
		if !strings.Contains(out, fragment) {
			t.Errorf("output = %s\nwant it to contain %s", out, fragment)
		}
	}
}

func TestCheckLocale(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = B\n")

	out, err := execCheck(t, "--locale=ja", root)
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	if !strings.Contains(out, "未定義の名前: B") {
		t.Errorf("output = %q, want the ja rendering", out)
	}
}

func TestCheckProfile(t *testing.T) {
	// --profile selects a [profile.<name>] entry; the default stays on the
	// top-level one. The default entry is clean and the editor entry broken,
	// so the flag's effect is observable.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n\n[profile.editor]\nentry = \"editor.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")
	belttest.WriteFile(t, root, "editor.belt", "const E = F\n")

	if out, err := execCheck(t, root); err != nil {
		t.Fatalf("check (default profile) = %v\n%s", err, out)
	}
	out, err := execCheck(t, "--profile=editor", root)
	if err == nil {
		t.Fatalf("check --profile=editor succeeded, want an error\n%s", out)
	}
	if !strings.Contains(out, "editor.belt:1:11: error[belt.semantic.undefined_name]") {
		t.Errorf("output = %q, want the editor entry's diagnostic", out)
	}
}

func TestCheckUnknownProfile(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

	out, err := execCheck(t, "--profile=editor", root)
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	if !strings.Contains(out, "error[project.config.unknown_profile]: profile editor is not defined") {
		t.Errorf("output = %q, want the unknown_profile diagnostic", out)
	}
}

func TestCheckUnknownReporter(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

	if out, err := execCheck(t, "--reporter=yaml", root); err == nil || !strings.Contains(err.Error(), "unknown reporter") {
		t.Errorf("check = %v, want an unknown-reporter error\n%s", err, out)
	}
}

func TestCheckExplicitFile(t *testing.T) {
	// An explicit file is checked ad hoc: no manifest required.
	dir := t.TempDir()
	belttest.WriteFile(t, dir, "lone.belt", "const A = B\n")

	out, err := execCheck(t, filepath.Join(dir, "lone.belt"))
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	if !strings.Contains(out, "error[belt.semantic.undefined_name]") {
		t.Errorf("output = %q, want the undefined_name diagnostic", out)
	}
}
