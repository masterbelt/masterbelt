package diagnostic

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This test is the run-time guard for the diagnostic catalog's parity: it binds
// code.csv, messages/en.csv, and messages/ja.csv together at `go test` time so
// the catalog cannot drift the way it once did: a code missing from the ja
// catalog, which compiled and silently rendered in English. The generator's
// validate() enforces the same invariants, but it runs only under
// `make generate`; this test runs in CI and on every `go test ./...`, catching
// a hand-edit of a CSV that forgets to regenerate, or a regenerate whose
// catalog_gen.go was never committed (the renderers map below is read straight
// from the compiled package and must agree with the CSVs).
//
// The invariants:
//   - the code set is identical across code.csv and every locale catalog;
//   - every message interpolates exactly its code's declared fields — no
//     missing placeholder, no undeclared one — in every locale;
//   - every declared code has a compiled renderer (catalog_gen.go is in sync).

// catalogPlaceholderRE matches a {field} interpolation. It mirrors the
// generator's placeholderRE (internal/gen/main.go); the two must stay identical
// so the test validates the same template grammar the generator compiles.
var catalogPlaceholderRE = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// readCatalogCSV reads a catalog CSV, skips the header row, and returns the data
// rows, each required to have exactly cols fields.
func readCatalogCSV(t *testing.T, path string, cols int) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	r := csv.NewReader(strings.NewReader(string(data)))
	r.FieldsPerRecord = cols
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(rows) == 0 {
		t.Fatalf("%s: empty (no header)", path)
	}
	return rows[1:] // drop the header
}

// codeFields reads code.csv and returns, per code, the set of field names it
// declares (the placeholders its messages must use).
func codeFields(t *testing.T) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	for _, row := range readCatalogCSV(t, "code.csv", 3) {
		code, fieldsCol := row[0], row[2]
		if _, dup := out[code]; dup {
			t.Errorf("code.csv: duplicate code %q", code)
		}
		fields := map[string]bool{}
		for _, tok := range strings.Fields(fieldsCol) {
			name, _, ok := strings.Cut(tok, ":")
			if !ok {
				t.Errorf("code.csv: code %q: field %q is not name:type", code, tok)
				continue
			}
			fields[name] = true
		}
		out[code] = fields
	}
	return out
}

// localeMessages reads messages/<locale>.csv and returns, per code, its message
// template. It is the source the test compares against the renderers compiled
// into catalog_gen.go.
func localeMessages(t *testing.T, locale string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, row := range readCatalogCSV(t, filepath.Join("messages", locale+".csv"), 2) {
		code, msg := row[0], row[1]
		if _, dup := out[code]; dup {
			t.Errorf("%s.csv: duplicate code %q", locale, code)
		}
		out[code] = msg
	}
	return out
}

// allLocales returns every locale catalog's base name (e.g. "en", "ja") so the
// parity checks cover whatever locales the messages directory holds, not a
// hard-coded pair.
func allLocales(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("messages", "*.csv"))
	if err != nil {
		t.Fatalf("glob messages: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no locale catalogs under messages/")
	}
	locales := make([]string, 0, len(paths))
	for _, p := range paths {
		locales = append(locales, strings.TrimSuffix(filepath.Base(p), ".csv"))
	}
	sort.Strings(locales)
	return locales
}

func placeholdersOf(msg string) map[string]bool {
	used := map[string]bool{}
	for _, m := range catalogPlaceholderRE.FindAllStringSubmatch(msg, -1) {
		used[m[1]] = true
	}
	return used
}

// TestCatalogCodeSetParity binds the code set across code.csv and every locale
// catalog: each must declare exactly the same codes. A code added to code.csv
// but not to a locale (or vice versa) fails here — the exact shape of the
// regression that motivated this guard.
func TestCatalogCodeSetParity(t *testing.T) {
	declared := codeFields(t)
	if len(declared) == 0 {
		t.Fatal("code.csv declares no codes")
	}

	for _, locale := range allLocales(t) {
		msgs := localeMessages(t, locale)

		for code := range declared {
			if _, ok := msgs[code]; !ok {
				t.Errorf("code %q is declared in code.csv but missing from %s.csv", code, locale)
			}
		}
		for code := range msgs {
			if _, ok := declared[code]; !ok {
				t.Errorf("%s.csv has a message for %q which code.csv does not declare", locale, code)
			}
		}
	}
}

// TestCatalogPlaceholderParity binds each message's interpolations to its code's
// declared fields: every locale's template for a code must use exactly the field
// set code.csv declares — no missing field, no undeclared placeholder — so the
// templates agree with each other and with the typed constructor's fields.
func TestCatalogPlaceholderParity(t *testing.T) {
	declared := codeFields(t)

	for _, locale := range allLocales(t) {
		for code, msg := range localeMessages(t, locale) {
			fields, ok := declared[code]
			if !ok {
				continue // reported by the code-set parity test
			}
			used := placeholdersOf(msg)
			for name := range fields {
				if !used[name] {
					t.Errorf("%s.csv: code %q declares field %q but its message does not use {%s}", locale, code, name, name)
				}
			}
			for name := range used {
				if !fields[name] {
					t.Errorf("%s.csv: code %q message references {%s} which is not a declared field", locale, code, name)
				}
			}
		}
	}
}

// TestCatalogRenderersInSync binds the CSVs to the compiled catalog_gen.go: every
// declared code must have a renderer in the in-memory renderers map, and the map
// must hold no code the CSVs do not declare. This catches a CSV edited without
// rerunning the generator, or a regenerate whose catalog_gen.go was never
// committed.
func TestCatalogRenderersInSync(t *testing.T) {
	declared := codeFields(t)

	for code := range declared {
		if _, ok := renderers[Code(code)]; !ok {
			t.Errorf("code %q has no compiled renderer; catalog_gen.go is stale (run make generate)", code)
		}
	}
	for code := range renderers {
		if _, ok := declared[string(code)]; !ok {
			t.Errorf("renderers has %q which code.csv does not declare; catalog_gen.go is stale", code)
		}
	}
}

// TestCatalogPlaceholderREMatchesGenerator pins this test's placeholder grammar
// to the generator's. If the generator's placeholderRE ever changes, this
// reminder (and the literal below) must change with it, so the two layers keep
// validating the same template syntax.
func TestCatalogPlaceholderREMatchesGenerator(t *testing.T) {
	const generatorPattern = `\{([a-zA-Z_][a-zA-Z0-9_]*)\}`
	if catalogPlaceholderRE.String() != generatorPattern {
		t.Fatalf("placeholder regexp drifted from the generator: %q != %q", catalogPlaceholderRE.String(), generatorPattern)
	}
}
