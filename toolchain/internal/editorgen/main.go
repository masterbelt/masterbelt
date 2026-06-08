// Command editorgen generates the VS Code editor configuration for masterbelt —
// the TextMate grammar and the language-configuration — from the lexer's own
// definitions (the keyword table and comment markers in package token), so none
// of it is maintained by hand.
//
// The grammar is only a "cold start" approximation (the lexical basics that
// colour a file before the language server responds); the accurate highlighting
// comes from the server's semantic tokens, which read the same parse. So the
// colours do not shift when the server comes up, every scope here is chosen to
// be the one VS Code falls back to for the matching semantic token type
// (keyword -> keyword.control, operator -> keyword.operator, ...), and the one
// thing lexing cannot decide — an identifier's role (type, reference,
// declaration) — is deliberately left uncoloured for the semantic tokens to
// enrich. The language-configuration's comment settings are derived from the
// same comment markers the lexer scans.
//
//go:generate go run .
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Output paths, relative to this package's directory (where `go generate` runs).
// editorgen now lives outside any single editor's tree (it is a multi-target
// generator), so the VS Code outputs are reached through toolchain/editors.
const (
	grammarPath  = "../../editors/vscode/syntaxes/masterbelt.tmLanguage.json"
	langConfPath = "../../editors/vscode/language-configuration.json"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "editorgen:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := writeJSON(grammarPath, buildGrammar()); err != nil {
		return err
	}
	return writeJSON(langConfPath, buildLanguageConfig())
}

// --- TextMate grammar ---------------------------------------------------------

type grammar struct {
	Schema     string               `json:"$schema"`
	Name       string               `json:"name"`
	ScopeName  string               `json:"scopeName"`
	Patterns   []rule               `json:"patterns"`
	Repository map[string]ruleGroup `json:"repository"`
}

type ruleGroup struct {
	Patterns []rule `json:"patterns"`
}

type rule struct {
	Include  string `json:"include,omitempty"`
	Name     string `json:"name,omitempty"`
	Match    string `json:"match,omitempty"`
	Begin    string `json:"begin,omitempty"`
	End      string `json:"end,omitempty"`
	Patterns []rule `json:"patterns,omitempty"`
}

// The core regexes for the literal tokens — the lexical shapes the lexer
// commits on, with no anchoring of their own. Both targets build on these: the
// TextMate grammar wraps them in word boundaries (it has no maximal munch),
// while the tree-sitter lexical layer (next) uses them bare and leans on
// tree-sitter's longest-match. Keeping the shapes in one place is what lets the
// two grammars share a lexer truth instead of each transcribing it.
const (
	reInteger  = `[0-9]+`
	reDatetime = `D[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{3})?(Z|[+-][0-9]{2}:[0-9]{2})`
	reDuration = `([0-9]+(ms|w|d|h|m|s))+`
	reEscape   = `\\(u\{[0-9A-Fa-f]{1,6}\}|[nrt0\\"])`
)

func buildGrammar() grammar {
	return grammar{
		Schema:    "https://raw.githubusercontent.com/martinring/tmlanguage/master/tmlanguage.json",
		Name:      langName,
		ScopeName: "source." + langName,
		Patterns: []rule{
			{Include: "#comments"},
			{Include: "#keywords"},
			{Include: "#modifiers"},
			// The datetime/duration literals must precede #numbers, or their
			// digit runs would half-match as plain integers.
			{Include: "#datetimes"},
			{Include: "#durations"},
			{Include: "#numbers"},
			{Include: "#strings"},
			{Include: "#operators"},
			// Identifiers are deliberately not matched: whether one is a type,
			// a reference, or a declaration name is not a lexical fact, so the
			// cold start leaves them uncoloured and the server's semantic
			// tokens enrich them — never correct them.
		},
		Repository: map[string]ruleGroup{
			"comments": {Patterns: []rule{
				{Name: lexCommentDoc.tmScope(), Match: regexp.QuoteMeta(token.DocCommentPrefix) + `.*$`},
				{Name: lexCommentLine.tmScope(), Match: regexp.QuoteMeta(token.LineCommentPrefix) + `.*$`},
				{Name: lexCommentBlock.tmScope(), Begin: regexp.QuoteMeta(token.BlockCommentOpen), End: regexp.QuoteMeta(token.BlockCommentClose)},
			}},
			// keyword.control is where VS Code lands a semantic `keyword`
			// token when the theme defines no semanticTokenColors, so the
			// keywords wear the same colour before and after the server is up.
			"keywords": {Patterns: []rule{
				{Name: lexKeyword.tmScope(), Match: keywordPattern()},
			}},
			// The accessor/static modifiers (get, set, static) are context
			// keywords: the lexer leaves them identifiers, so they are not in the
			// keyword table and would not flow into #keywords. This approximates
			// their modifier position — get/set followed by an identifier (the
			// property name), static followed by fn or a name — and colours them
			// keyword.control to match the server's semantic `keyword` token. A
			// get/set/static used as an ordinary name (the prelude's list.get(i))
			// is not followed by that shape, so it is left uncoloured, as the
			// server leaves it. The semantic tokens are authoritative; this is the
			// cold-start approximation.
			"modifiers": {Patterns: []rule{
				{Name: lexKeyword.tmScope(), Match: `\bstatic\b(?=\s+fn\b)`},
				{Name: lexKeyword.tmScope(), Match: `\b(get|set)\b(?=\s+[A-Za-z_])`},
			}},
			// A D-prefixed ISO-8601 instant: the same shape the lexer commits
			// on, milliseconds and signed offsets included. The semantic
			// `number` token lands on constant.numeric, so the literal wears
			// the same colour before and after the server is up.
			"datetimes": {Patterns: []rule{
				{Name: lexNumberDatetime.tmScope(), Match: `\b` + reDatetime},
			}},
			// Concatenated digit+unit groups (3w4d5h6m7s8ms); ms is listed
			// before its prefix letters so the alternation munches maximally,
			// exactly as the lexer does.
			"durations": {Patterns: []rule{
				{Name: lexNumberDuration.tmScope(), Match: `\b` + reDuration + `\b`},
			}},
			"numbers": {Patterns: []rule{
				{Name: lexNumberInteger.tmScope(), Match: `\b` + reInteger + `\b`},
			}},
			// A double-quoted string with the escapes the lexer recognizes —
			// \n \r \t \0 \\ \" and \u{...}. Only a cold-start approximation; the
			// server's semantic tokens are authoritative.
			"strings": {Patterns: []rule{
				{
					Name:  lexString.tmScope(),
					Begin: `"`,
					End:   `"`,
					Patterns: []rule{
						{Name: lexStringEscape.tmScope(), Match: reEscape},
					},
				},
			}},
			// The spellings the server classifies as `operator` sit under
			// keyword.operator, their semantic fallback scope. The range operators
			// "..." and ".." lead so the longer one wins the cold-start match (the
			// grammar has no maximal munch of its own); the arrow is listed before
			// "=" and ":" only for clarity. None of the rest overlaps another's
			// first byte. The range operators escape to "\.\.\." and "\.\." but
			// must not match a member-access "." beside a number, so each is the
			// exact two- or three-dot run.
			"operators": {Patterns: []rule{
				{Name: lexOperatorRange.tmScope(), Match: regexp.QuoteMeta(token.DotDotDot.Symbol())},
				{Name: lexOperatorRange.tmScope(), Match: regexp.QuoteMeta(token.DotDot.Symbol())},
				{Name: lexOperator.tmScope(), Match: regexp.QuoteMeta(token.Arrow.Symbol())},
				{Name: lexOperatorAssign.tmScope(), Match: `=`},
				{Name: lexOperator.tmScope(), Match: `:`},
			}},
		},
	}
}

// keywordPattern builds a word-bounded alternation of the language's keywords,
// e.g. `\b(const|pub)\b`, from the single source of truth in package token.
func keywordPattern() string {
	kws := token.Keywords()
	escaped := make([]string, len(kws))
	for i, kw := range kws {
		escaped[i] = regexp.QuoteMeta(kw)
	}
	return `\b(` + strings.Join(escaped, "|") + `)\b`
}

// --- language-configuration ---------------------------------------------------

type languageConfig struct {
	Comments         commentConfig `json:"comments"`
	Brackets         [][]string    `json:"brackets"`
	AutoClosingPairs []pair        `json:"autoClosingPairs"`
	SurroundingPairs []pair        `json:"surroundingPairs"`
}

type commentConfig struct {
	LineComment  string    `json:"lineComment"`
	BlockComment [2]string `json:"blockComment"`
}

type pair struct {
	Open  string `json:"open"`
	Close string `json:"close"`
}

func buildLanguageConfig() languageConfig {
	// The bracket pairs the language uses: parentheses (calls, parameter and
	// function-type lists), braces (records, impl and method blocks), and square
	// brackets (list and map literals). Their spellings come from the token
	// package, so they cannot drift from the lexer. Each pair drives bracket
	// matching, auto-closing, and surround-selection.
	bracketPairs := [][2]string{
		{token.LParen.Symbol(), token.RParen.Symbol()},
		{token.LBrace.Symbol(), token.RBrace.Symbol()},
		{token.LBracket.Symbol(), token.RBracket.Symbol()},
	}

	brackets := make([][]string, 0, len(bracketPairs))
	autoClosing := make([]pair, 0, 2+len(bracketPairs))
	autoClosing = append(autoClosing,
		pair{Open: token.BlockCommentOpen, Close: token.BlockCommentClose},
		pair{Open: `"`, Close: `"`})
	surrounding := make([]pair, 0, 1+len(bracketPairs))
	surrounding = append(surrounding, pair{Open: `"`, Close: `"`})
	for _, bp := range bracketPairs {
		brackets = append(brackets, []string{bp[0], bp[1]})
		autoClosing = append(autoClosing, pair{Open: bp[0], Close: bp[1]})
		surrounding = append(surrounding, pair{Open: bp[0], Close: bp[1]})
	}

	return languageConfig{
		Comments: commentConfig{
			LineComment:  token.LineCommentPrefix,
			BlockComment: [2]string{token.BlockCommentOpen, token.BlockCommentClose},
		},
		Brackets:         brackets,
		AutoClosingPairs: autoClosing,
		SurroundingPairs: surrounding,
	}
}

// --- shared -------------------------------------------------------------------

func writeJSON(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}
