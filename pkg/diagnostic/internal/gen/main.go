// Command gen turns the diagnostic tables (code.csv and messages/<locale>.csv)
// into compiled Go: a typed constructor per code in each owning package, and a
// per-code message renderer keyed by locale in the diagnostic package. It is
// invoked by `go generate ./...` from the diagnostic package directory.
//
// code.csv columns:   code,severity,fields
//
//	code     dotted identifier; all but the last segment name the owning
//	         package by its path under pkg/
//	         (masterbelt.lexer.unexpected_character -> package
//	         pkg/masterbelt/lexer, code "unexpected_character";
//	         project.config.missing -> package pkg/project/config)
//	severity error | warning | info | hint
//	fields   space-separated name:type pairs (e.g. "char:rune", "start:int end:int")
//
// messages/<locale>.csv columns: code,message  (message may interpolate {field})
//
// Nothing is parsed at run time: every message template is compiled into a
// renderer func that switches on locale, so locales remain swappable while
// avoiding any runtime catalog parsing.
package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// diagnosticPkg is the import path of the diagnostic package itself, which
	// every generated constructor file imports.
	diagnosticPkg = "github.com/masterbelt/masterbelt/pkg/diagnostic"
	// pkgPrefix is the import-path prefix of pkg/, the tree that owns every
	// generated diagnostic_gen.go: a code's package path mirrors its owner's
	// location under pkg/ (masterbelt.lexer.* -> pkg/masterbelt/lexer,
	// project.config.* -> pkg/project/config). pkgDir is the same tree on
	// disk, relative to this (the diagnostic) package, where those files are
	// written.
	pkgPrefix = "github.com/masterbelt/masterbelt/pkg"
	pkgDir    = ".."

	defaultLocale = "en"
)

// typeWrappers maps a declared field type to the diagnostic wrapper that adapts
// it to fmt.Stringer.
var typeWrappers = map[string]string{
	"rune":   "diagnostic.Rune",
	"int":    "diagnostic.Int",
	"string": "diagnostic.Str",
}

var severityConsts = map[string]string{
	"error":   "Error",
	"warning": "Warning",
	"info":    "Info",
	"hint":    "Hint",
}

type field struct {
	name string
	typ  string
}

type codeDef struct {
	code      string
	severity  string // diagnostic.<X> constant name
	fields    []field
	pkgName   string // Go package identifier of the owner
	pkgImport string // owner's full import path
	relDir    string // owner's directory, relative to the diagnostic package
	constName string // CodeXxx
	ctorName  string // newXxxDiagnostic
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "diagnostic gen:", err)
		os.Exit(1)
	}
}

func run() error {
	codes, err := readCodes("code.csv")
	if err != nil {
		return err
	}
	locales, err := readLocales("messages")
	if err != nil {
		return err
	}
	if err := validate(codes, locales); err != nil {
		return err
	}

	// Per-package constructors and code constants.
	byDir := map[string][]codeDef{}
	for _, c := range codes {
		byDir[c.relDir] = append(byDir[c.relDir], c)
	}
	for dir, defs := range byDir {
		sort.Slice(defs, func(i, j int) bool { return defs[i].code < defs[j].code })
		src, err := generatePackage(defs)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		if err := writeGo(filepath.Join(dir, "diagnostic_gen.go"), src); err != nil {
			return err
		}
	}

	// The locale-aware renderer catalog, in the diagnostic package itself.
	catalog, err := generateCatalog(codes, locales)
	if err != nil {
		return err
	}
	return writeGo("catalog_gen.go", catalog)
}

func writeGo(path string, src []byte) error {
	if err := os.WriteFile(path, src, 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

func readCodes(path string) ([]codeDef, error) {
	rows, err := readCSV(path, 3)
	if err != nil {
		return nil, err
	}
	var defs []codeDef
	for _, row := range rows {
		code, sev, fieldsCol := row[0], row[1], row[2]

		sevConst, ok := severityConsts[sev]
		if !ok {
			return nil, fmt.Errorf("code %q: unknown severity %q", code, sev)
		}
		fields, err := parseFields(code, fieldsCol)
		if err != nil {
			return nil, err
		}

		parts := strings.Split(code, ".")
		if len(parts) < 2 {
			return nil, fmt.Errorf("code %q: must be <pkg>....<name>, the owner's path under pkg/", code)
		}
		pkgParts := parts[:len(parts)-1] // drop the code name
		name := parts[len(parts)-1]

		defs = append(defs, codeDef{
			code:      code,
			severity:  sevConst,
			fields:    fields,
			pkgName:   pkgParts[len(pkgParts)-1],
			pkgImport: pkgPrefix + "/" + strings.Join(pkgParts, "/"),
			relDir:    filepath.Join(append([]string{pkgDir}, pkgParts...)...),
			constName: "Code" + camel(name),
			ctorName:  "new" + camel(name) + "Diagnostic",
		})
	}
	return defs, nil
}

func parseFields(code, col string) ([]field, error) {
	var fields []field
	for tok := range strings.FieldsSeq(col) {
		name, typ, ok := strings.Cut(tok, ":")
		if !ok {
			return nil, fmt.Errorf("code %q: field %q must be name:type", code, tok)
		}
		if _, known := typeWrappers[typ]; !known {
			return nil, fmt.Errorf("code %q: unsupported field type %q", code, typ)
		}
		fields = append(fields, field{name: name, typ: typ})
	}
	return fields, nil
}

// readLocales reads every messages/<locale>.csv file and returns, per locale, a
// map from code to its message template.
func readLocales(dir string) (map[string]map[string]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.csv"))
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, path := range paths {
		locale := strings.TrimSuffix(filepath.Base(path), ".csv")
		rows, err := readCSV(path, 2)
		if err != nil {
			return nil, err
		}
		msgs := map[string]string{}
		for _, row := range rows {
			msgs[row[0]] = row[1]
		}
		out[locale] = msgs
	}
	return out, nil
}

var placeholderRE = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// validate guards that codes and messages line up: every locale (not only the
// default) covers every code, no locale references an unknown code, and every
// message interpolates precisely the fields declared for its code — no missing,
// no extras — so all locales stay consistent. The completeness check is applied
// to every locale uniformly, so dropping a row from a non-default catalog (the
// E-16 ja regression: a message silently falling back to English) fails the
// build rather than slipping through.
func validate(codes []codeDef, locales map[string]map[string]string) error {
	declared := map[string]codeDef{}
	for _, c := range codes {
		declared[c.code] = c
	}

	if _, ok := locales[defaultLocale]; !ok {
		return fmt.Errorf("missing default locale catalog messages/%s.csv", defaultLocale)
	}

	// Iterate locale names in a stable order so a failure is deterministic.
	localeNames := make([]string, 0, len(locales))
	for locale := range locales {
		localeNames = append(localeNames, locale)
	}
	sort.Strings(localeNames)

	for _, locale := range localeNames {
		msgs := locales[locale]
		// Every declared code must have a message in this locale: a code present
		// in code.csv but missing from a locale catalog would render in the
		// default language with no signal.
		for _, c := range codes {
			if _, ok := msgs[c.code]; !ok {
				return fmt.Errorf("code %q has no message in %s.csv", c.code, locale)
			}
		}
		// No locale may reference a code that code.csv does not declare, and
		// every message must interpolate exactly its code's declared fields.
		for code, msg := range msgs {
			c, ok := declared[code]
			if !ok {
				return fmt.Errorf("%s.csv: message for unknown code %q (not in code.csv)", locale, code)
			}
			if err := checkFields(locale, c, msg); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkFields(locale string, c codeDef, msg string) error {
	used := map[string]bool{}
	for _, m := range placeholderRE.FindAllStringSubmatch(msg, -1) {
		used[m[1]] = true
	}
	declaredFields := map[string]bool{}
	for _, f := range c.fields {
		declaredFields[f.name] = true
		if !used[f.name] {
			return fmt.Errorf("%s.csv: code %q: field %q is declared but not used in its message", locale, c.code, f.name)
		}
	}
	for name := range used {
		if !declaredFields[name] {
			return fmt.Errorf("%s.csv: code %q: message references {%s} which is not a declared field", locale, c.code, name)
		}
	}
	return nil
}

func generatePackage(defs []codeDef) ([]byte, error) {
	pkgName := defs[0].pkgName

	needsFmt := false
	for _, d := range defs {
		if len(d.fields) > 0 {
			needsFmt = true
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by \"go generate\"; DO NOT EDIT.\n\npackage %s\n\n", pkgName)
	b.WriteString("import (\n")
	if needsFmt {
		b.WriteString("\t\"fmt\"\n\n")
	}
	fmt.Fprintf(&b, "\t%q\n", diagnosticPkg)
	b.WriteString(")\n\n")

	b.WriteString("const (\n")
	for _, d := range defs {
		fmt.Fprintf(&b, "\t%s diagnostic.Code = %q\n", d.constName, d.code)
	}
	b.WriteString(")\n")

	for _, d := range defs {
		params := make([]string, 0, 2+len(d.fields))
		params = append(params, "offset int", "width int")
		for _, f := range d.fields {
			params = append(params, f.name+" "+f.typ)
		}
		fmt.Fprintf(&b, "\nfunc %s(%s) diagnostic.Diagnostic {\n", d.ctorName, strings.Join(params, ", "))

		fieldsRef := "nil"
		if len(d.fields) > 0 {
			fieldsRef = "fields"
			b.WriteString("\tfields := map[string]fmt.Stringer{\n")
			for _, f := range d.fields {
				fmt.Fprintf(&b, "\t\t%q: %s(%s),\n", f.name, typeWrappers[f.typ], f.name)
			}
			b.WriteString("\t}\n")
		}
		b.WriteString("\treturn diagnostic.Diagnostic{\n")
		fmt.Fprintf(&b, "\t\tSeverity: diagnostic.%s,\n", d.severity)
		fmt.Fprintf(&b, "\t\tCode:     %s,\n", d.constName)
		fmt.Fprintf(&b, "\t\tMessage:  diagnostic.Render(diagnostic.DefaultLocale, %s, %s),\n", d.constName, fieldsRef)
		fmt.Fprintf(&b, "\t\tFields:   %s,\n", fieldsRef)
		b.WriteString("\t\tOffset:   offset,\n")
		b.WriteString("\t\tWidth:    width,\n")
		b.WriteString("\t}\n}\n")
	}

	return format.Source([]byte(b.String()))
}

// generateCatalog writes the diagnostic package's renderers map: one closure per
// code that switches on locale and concatenates the message from the fields.
func generateCatalog(codes []codeDef, locales map[string]map[string]string) ([]byte, error) {
	tmplByCode := map[string]map[string]string{} // code -> locale -> template
	for locale, msgs := range locales {
		for code, msg := range msgs {
			if tmplByCode[code] == nil {
				tmplByCode[code] = map[string]string{}
			}
			tmplByCode[code][locale] = msg
		}
	}

	sorted := append([]codeDef(nil), codes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].code < sorted[j].code })

	var b strings.Builder
	b.WriteString("// Code generated by \"go generate\"; DO NOT EDIT.\n\npackage diagnostic\n\n")
	b.WriteString("import \"fmt\"\n\n")
	b.WriteString("// renderers maps each diagnostic code to a function that renders its message\n")
	b.WriteString("// in a given locale from the diagnostic's fields.\n")
	b.WriteString("var renderers = map[Code]func(Locale, map[string]fmt.Stringer) string{\n")
	for _, d := range sorted {
		byLoc := tmplByCode[d.code]
		fmt.Fprintf(&b, "\t%q: func(loc Locale, f map[string]fmt.Stringer) string {\n", d.code)

		var others []string
		for loc := range byLoc {
			if loc != defaultLocale {
				others = append(others, loc)
			}
		}
		sort.Strings(others)

		if len(others) == 0 {
			fmt.Fprintf(&b, "\t\treturn %s\n", renderExpr(byLoc[defaultLocale]))
		} else {
			b.WriteString("\t\tswitch loc {\n")
			for _, loc := range others {
				fmt.Fprintf(&b, "\t\tcase %q:\n\t\t\treturn %s\n", loc, renderExpr(byLoc[loc]))
			}
			fmt.Fprintf(&b, "\t\tdefault:\n\t\t\treturn %s\n", renderExpr(byLoc[defaultLocale]))
			b.WriteString("\t\t}\n")
		}
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n")

	return format.Source([]byte(b.String()))
}

// renderExpr turns a message template into a Go string expression that
// concatenates quoted literal runs with f["name"].String() lookups.
func renderExpr(tmpl string) string {
	var parts []string
	last := 0
	for _, loc := range placeholderRE.FindAllStringSubmatchIndex(tmpl, -1) {
		if loc[0] > last {
			parts = append(parts, strconv.Quote(tmpl[last:loc[0]]))
		}
		parts = append(parts, fmt.Sprintf("f[%q].String()", tmpl[loc[2]:loc[3]]))
		last = loc[1]
	}
	if last < len(tmpl) {
		parts = append(parts, strconv.Quote(tmpl[last:]))
	}
	if len(parts) == 0 {
		return `""`
	}
	return strings.Join(parts, " + ")
}

// readCSV reads path, skips the header row, and returns the data rows, each with
// exactly cols fields.
func readCSV(path string, cols int) ([][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = cols
	var rows [][]string
	first := true
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if first {
			first = false
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// camel converts a snake_case identifier to UpperCamelCase.
func camel(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}
