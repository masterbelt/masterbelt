package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masterbelt/masterbelt/internal/belttest"
)

// execFmt runs `masterbelt fmt args...` with stdin as the input stream, and
// returns the command's stdout and stderr separately plus its error. The fmt
// flags persist on the command between Execute calls, so they are reset on
// cleanup.
func execFmt(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	RootCmd.SetOut(&out)
	RootCmd.SetErr(&errb)
	RootCmd.SetIn(strings.NewReader(stdin))
	RootCmd.SetArgs(append([]string{"fmt"}, args...))
	t.Cleanup(func() {
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
		RootCmd.SetIn(nil)
		RootCmd.SetArgs(nil)
		_ = FmtCmd.Flags().Set("write", "false")
		_ = FmtCmd.Flags().Set("check", "false")
		_ = FmtCmd.Flags().Set("diff", "false")
		_ = FmtCmd.Flags().Set("stdin-filepath", "")
	})
	err = RootCmd.Execute()
	return out.String(), errb.String(), err
}

const messy = "const x = 1   \n\n\n"
const canonical = "const x = 1\n"

func TestFmtStdout(t *testing.T) {
	dir := t.TempDir()
	belttest.WriteFile(t, dir, "a.belt", messy)
	path := filepath.Join(dir, "a.belt")

	out, _, err := execFmt(t, "", path)
	if err != nil {
		t.Fatalf("fmt = %v", err)
	}
	if out != canonical {
		t.Errorf("stdout = %q, want %q", out, canonical)
	}
	// Default mode never touches the file.
	if got, _ := os.ReadFile(path); string(got) != messy {
		t.Errorf("file changed to %q, want it untouched", got)
	}
}

func TestFmtStdin(t *testing.T) {
	out, _, err := execFmt(t, messy, "-")
	if err != nil {
		t.Fatalf("fmt = %v", err)
	}
	if out != canonical {
		t.Errorf("stdout = %q, want %q", out, canonical)
	}
}

func TestFmtWrite(t *testing.T) {
	dir := t.TempDir()
	belttest.WriteFile(t, dir, "a.belt", messy)
	path := filepath.Join(dir, "a.belt")

	out, _, err := execFmt(t, "", "-w", path)
	if err != nil {
		t.Fatalf("fmt -w = %v", err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want none for -w", out)
	}
	if got, _ := os.ReadFile(path); string(got) != canonical {
		t.Errorf("file = %q, want %q", got, canonical)
	}
}

// TestFmtWriteOnlyWhenChanged pins that -w leaves an already-canonical file
// untouched on disk: its modification time does not move.
func TestFmtWriteOnlyWhenChanged(t *testing.T) {
	dir := t.TempDir()
	belttest.WriteFile(t, dir, "a.belt", canonical)
	path := filepath.Join(dir, "a.belt")

	old := time.Unix(1_000_000, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execFmt(t, "", "-w", path); err != nil {
		t.Fatalf("fmt -w = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("file was rewritten (mtime moved to %v); a canonical file should be left alone", info.ModTime())
	}
}

func TestFmtCheck(t *testing.T) {
	dir := t.TempDir()
	belttest.WriteFile(t, dir, "a.belt", messy)
	path := filepath.Join(dir, "a.belt")

	out, _, err := execFmt(t, "", "--check", path)
	if err == nil {
		t.Fatal("fmt --check on an unformatted file succeeded, want non-zero")
	}
	if !strings.Contains(out, "a.belt") {
		t.Errorf("stdout = %q, want it to list a.belt", out)
	}
	// --check writes nothing back.
	if got, _ := os.ReadFile(path); string(got) != messy {
		t.Errorf("file changed to %q, want it untouched", got)
	}
}

func TestFmtCheckClean(t *testing.T) {
	dir := t.TempDir()
	belttest.WriteFile(t, dir, "a.belt", canonical)
	path := filepath.Join(dir, "a.belt")

	out, _, err := execFmt(t, "", "--check", path)
	if err != nil {
		t.Fatalf("fmt --check on a formatted file = %v", err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want none for a formatted file", out)
	}
}

func TestFmtDiff(t *testing.T) {
	dir := t.TempDir()
	belttest.WriteFile(t, dir, "a.belt", messy)
	path := filepath.Join(dir, "a.belt")

	out, _, err := execFmt(t, "", "--diff", path)
	if err != nil {
		t.Fatalf("fmt --diff = %v", err)
	}
	for _, want := range []string{"@@", "-const x = 1   ", "+const x = 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff = %q, want it to contain %q", out, want)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != messy {
		t.Errorf("file changed to %q, want it untouched", got)
	}
}

func TestFmtWriteSkipsBrokenInput(t *testing.T) {
	dir := t.TempDir()
	broken := "const x = = (((\n"
	belttest.WriteFile(t, dir, "bad.belt", broken)
	path := filepath.Join(dir, "bad.belt")

	_, stderr, err := execFmt(t, "", "-w", path)
	if err != nil {
		t.Fatalf("fmt -w on a broken file = %v, want it to skip and succeed", err)
	}
	if !strings.Contains(stderr, "syntax errors") {
		t.Errorf("stderr = %q, want a syntax-errors warning", stderr)
	}
	if got, _ := os.ReadFile(path); string(got) != broken {
		t.Errorf("broken file was rewritten to %q, want it untouched", got)
	}
}

func TestFmtStdinWriteRejected(t *testing.T) {
	if _, _, err := execFmt(t, canonical, "-w", "-"); err == nil {
		t.Fatal("fmt -w - succeeded, want an error: stdin cannot be written back")
	}
}

func TestFmtConflictingModes(t *testing.T) {
	if _, _, err := execFmt(t, "", "-w", "--check", "x.belt"); err == nil {
		t.Fatal("fmt -w --check succeeded, want an error: modes are exclusive")
	}
}

// TestFmtStdinFilepathLayout drives the .editorconfig integration end to end
// through the CLI: a crlf config resolved against --stdin-filepath makes the
// stdin output use CRLF line endings.
func TestFmtStdinFilepathLayout(t *testing.T) {
	dir := t.TempDir()
	belttest.WriteFile(t, dir, ".editorconfig", "root = true\n[*.belt]\nindent_style = space\nindent_size = 2\nend_of_line = crlf\n")
	stdinPath := filepath.Join(dir, "x.belt")

	out, _, err := execFmt(t, messy, "--stdin-filepath", stdinPath, "-")
	if err != nil {
		t.Fatalf("fmt = %v", err)
	}
	if out != "const x = 1\r\n" {
		t.Errorf("stdout = %q, want CRLF-terminated output from the .editorconfig", out)
	}
}

func TestFmtDirectoryWalk(t *testing.T) {
	dir := t.TempDir()
	belttest.WriteFile(t, dir, "a.belt", messy)
	belttest.WriteFile(t, dir, "sub/b.belt", messy)
	belttest.WriteFile(t, dir, "notbelt.txt", messy)

	out, _, err := execFmt(t, "", "--check", dir)
	if err == nil {
		t.Fatal("fmt --check over the directory succeeded, want non-zero")
	}
	for _, want := range []string{"a.belt", filepath.Join("sub", "b.belt")} {
		if !strings.Contains(out, want) {
			t.Errorf("listing = %q, want it to include %q", out, want)
		}
	}
	if strings.Contains(out, "notbelt.txt") {
		t.Errorf("listing = %q, should not include the non-.belt file", out)
	}
}

func TestFmtProjectNoArgs(t *testing.T) {
	dir := t.TempDir()
	belttest.WriteFile(t, dir, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, dir, "main.belt", "const A = 1   \n")
	t.Chdir(dir)

	out, _, err := execFmt(t, "", "--check")
	if err == nil {
		t.Fatal("fmt --check on the unformatted project succeeded, want non-zero")
	}
	if !strings.Contains(out, "main.belt") {
		t.Errorf("listing = %q, want it to include main.belt", out)
	}
}

// TestFmtIdempotent pins the invariant: formatting a formatted result is a
// no-op. Format(Format(x)) == Format(x).
func TestFmtIdempotent(t *testing.T) {
	once, _, err := execFmt(t, messy, "-")
	if err != nil {
		t.Fatalf("first fmt = %v", err)
	}
	twice, _, err := execFmt(t, once, "-")
	if err != nil {
		t.Fatalf("second fmt = %v", err)
	}
	if once != twice {
		t.Errorf("not idempotent: first = %q, second = %q", once, twice)
	}
}
