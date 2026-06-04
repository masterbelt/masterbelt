package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCheck executes `masterbelt check args...` through the root command and
// returns its combined output.
func runCheck(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	RootCmd.SetOut(&out)
	RootCmd.SetErr(&out)
	RootCmd.SetArgs(append([]string{"check"}, args...))
	t.Cleanup(func() {
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
		RootCmd.SetArgs([]string{})
	})
	err := RootCmd.Execute()
	return out.String(), err
}

// write creates path under root (and any parent directories) with content.
func write(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckProject(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = \"src/main.belt\"\n")
	write(t, root, "src/main.belt", "const MaxLevel: int64 = 100\n")

	out, err := runCheck(t, root)
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
	write(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	write(t, root, "main.belt", "const A = 1\n")
	write(t, root, "sub/keep", "")
	t.Chdir(filepath.Join(root, "sub"))

	if out, err := runCheck(t); err != nil {
		t.Fatalf("check = %v\n%s", err, out)
	}
}

func TestCheckReportsSourceErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	write(t, root, "main.belt", "const A = B\n")

	out, err := runCheck(t, root)
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	// The diagnostic is anchored to the entry file with line and column.
	if !strings.Contains(out, "main.belt:1:11: error[masterbelt.semantic.undefined_name]: undefined name: B") {
		t.Errorf("output = %q, want the anchored undefined_name diagnostic", out)
	}
}

func TestCheckReportsManifestErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "name = \"broken\"\n")

	out, err := runCheck(t, root)
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
	out, err := runCheck(t, t.TempDir())
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	if !strings.Contains(out, "error[project.config.missing]") {
		t.Errorf("output = %q, want the missing-manifest diagnostic", out)
	}
}

func TestCheckExplicitFile(t *testing.T) {
	// An explicit file is checked ad hoc: no manifest required.
	dir := t.TempDir()
	write(t, dir, "lone.belt", "const A = B\n")

	out, err := runCheck(t, filepath.Join(dir, "lone.belt"))
	if err == nil {
		t.Fatalf("check succeeded, want an error\n%s", out)
	}
	if !strings.Contains(out, "error[masterbelt.semantic.undefined_name]") {
		t.Errorf("output = %q, want the undefined_name diagnostic", out)
	}
}
