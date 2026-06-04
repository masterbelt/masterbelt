package reporter

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
)

// schemaVersion is bumped when the JSON report shape changes, so downstream
// consumers (CI, agents, the MCP surface when it lands) can tell what they
// are reading.
const schemaVersion = 1

// JSON accumulates diagnostics and emits them on Flush as one machine-readable
// document, the `check --format=json` schema:
//
//	{
//	  "version": 1,
//	  "diagnostics": [
//	    {
//	      "code": "masterbelt.semantic.constant_overflow",
//	      "severity": "error",
//	      "file": "examples/bad.belt",
//	      "range": {"start": {"offset": 120, "line": 3, "column": 14},
//	                "end":   {"offset": 125, "line": 3, "column": 19}},
//	      "message": {"locale": "en", "text": "constant 70000 overflows int8"},
//	      "data": {"value": "70000", "typ": "int8"},
//	      "fixes": []
//	    }
//	  ]
//	}
//
// Machines read code + data — the stable identifier and the diagnostic's
// typed fields, both locale-independent, so no message parsing is ever
// needed; humans read message, rendered in the reporter's locale. Offsets are
// bytes; line and column are 1-based with byte-measured columns. Bare
// diagnostics carry neither file nor range. fixes is always present — empty
// until fix metadata lands — so consumers never branch on its absence, and
// anchor joins it when semantic anchors (A-5) land.
type JSON struct {
	w      io.Writer
	locale diagnostic.Locale
	diags  []jsonDiag
	errors errorCount
}

// NewJSON returns a JSON reporter writing to w, rendering messages in locale.
func NewJSON(w io.Writer, locale diagnostic.Locale) *JSON {
	return &JSON{w: w, locale: locale, diags: []jsonDiag{}}
}

// Report accumulates diags anchored to file, ordered by offset.
func (r *JSON) Report(file *source.File, diags []diagnostic.Diagnostic) {
	for _, d := range byOffset(diags) {
		jd := r.diag(d)
		jd.File = file.Name()
		jd.Range = &jsonRange{Start: position(file, d.Offset), End: position(file, d.End())}
		r.diags = append(r.diags, jd)
	}
	r.errors.add(diags)
}

// ReportBare accumulates diags that have no file to anchor to; they are
// emitted without file and range.
func (r *JSON) ReportBare(diags []diagnostic.Diagnostic) {
	for _, d := range diags {
		r.diags = append(r.diags, r.diag(d))
	}
	r.errors.add(diags)
}

// Errors reports how many error-severity diagnostics have been accumulated.
func (r *JSON) Errors() int {
	return int(r.errors)
}

// Flush writes the accumulated report as one indented JSON document.
func (r *JSON) Flush() error {
	out, err := json.MarshalIndent(jsonReport{Version: schemaVersion, Diagnostics: r.diags}, "", "  ")
	if err != nil {
		return err
	}
	_, err = r.w.Write(append(out, '\n'))
	return err
}

func (r *JSON) diag(d diagnostic.Diagnostic) jsonDiag {
	return jsonDiag{
		Code:     string(d.Code),
		Severity: d.Severity.String(),
		Message:  jsonMessage{Locale: string(r.locale), Text: message(d, r.locale)},
		Data:     data(d.Fields),
		Fixes:    []jsonFix{},
	}
}

func position(file *source.File, offset int) jsonPos {
	p := file.Position(offset)
	return jsonPos{Offset: p.ByteOffset, Line: p.Line, Column: p.Column}
}

// data flattens the diagnostic's typed fields to strings, keyed as declared
// in code.csv. encoding/json sorts the keys, so output stays deterministic.
func data(fields map[string]fmt.Stringer) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(fields))
	for name, value := range fields {
		out[name] = value.String()
	}
	return out
}

type jsonReport struct {
	Version     int        `json:"version"`
	Diagnostics []jsonDiag `json:"diagnostics"`
}

type jsonDiag struct {
	Code     string            `json:"code"`
	Severity string            `json:"severity"` // "error" | "warning" | "info" | "hint"
	File     string            `json:"file,omitempty"`
	Anchor   string            `json:"anchor,omitempty"` // supplied by A-5, once it lands
	Range    *jsonRange        `json:"range,omitempty"`
	Message  jsonMessage       `json:"message"`
	Data     map[string]string `json:"data,omitempty"`
	Fixes    []jsonFix         `json:"fixes"`
}

type jsonRange struct {
	Start jsonPos `json:"start"`
	End   jsonPos `json:"end"`
}

type jsonPos struct {
	Offset int `json:"offset"`
	Line   int `json:"line"`
	Column int `json:"column"`
}

type jsonMessage struct {
	Locale string `json:"locale"`
	Text   string `json:"text"`
}

// jsonFix is a suggested repair with an applicability label
// ("machine-applicable" | "maybe-incorrect" | "manual"). No fix provider
// exists yet; the type pins the schema so fixes can arrive without breaking
// consumers.
type jsonFix struct {
	Title         string     `json:"title"`
	Applicability string     `json:"applicability"`
	Edits         []jsonEdit `json:"edits"`
}

type jsonEdit struct {
	Anchor  string     `json:"anchor,omitempty"`
	Range   *jsonRange `json:"range,omitempty"`
	NewText string     `json:"new_text"`
}
