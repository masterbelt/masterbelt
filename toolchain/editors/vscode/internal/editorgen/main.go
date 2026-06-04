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
const (
	grammarPath  = "../../syntaxes/masterbelt.tmLanguage.json"
	langConfPath = "../../language-configuration.json"
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

func buildGrammar() grammar {
	return grammar{
		Schema:    "https://raw.githubusercontent.com/martinring/tmlanguage/master/tmlanguage.json",
		Name:      "masterbelt",
		ScopeName: "source.masterbelt",
		Patterns: []rule{
			{Include: "#comments"},
			{Include: "#keywords"},
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
				{Name: "comment.line.documentation.masterbelt", Match: regexp.QuoteMeta(token.DocCommentPrefix) + `.*$`},
				{Name: "comment.line.double-slash.masterbelt", Match: regexp.QuoteMeta(token.LineCommentPrefix) + `.*$`},
				{Name: "comment.block.masterbelt", Begin: regexp.QuoteMeta(token.BlockCommentOpen), End: regexp.QuoteMeta(token.BlockCommentClose)},
			}},
			// keyword.control is where VS Code lands a semantic `keyword`
			// token when the theme defines no semanticTokenColors, so the
			// keywords wear the same colour before and after the server is up.
			"keywords": {Patterns: []rule{
				{Name: "keyword.control.masterbelt", Match: keywordPattern()},
			}},
			"numbers": {Patterns: []rule{
				{Name: "constant.numeric.integer.masterbelt", Match: `\b[0-9]+\b`},
			}},
			// A double-quoted string with the escapes the lexer recognizes —
			// \n \r \t \0 \\ \" and \u{...}. Only a cold-start approximation; the
			// server's semantic tokens are authoritative.
			"strings": {Patterns: []rule{
				{
					Name:  "string.quoted.double.masterbelt",
					Begin: `"`,
					End:   `"`,
					Patterns: []rule{
						{Name: "constant.character.escape.masterbelt", Match: `\\(u\{[0-9A-Fa-f]{1,6}\}|[nrt0\\"])`},
					},
				},
			}},
			// Both spellings the server classifies as `operator` sit under
			// keyword.operator, their semantic fallback scope.
			"operators": {Patterns: []rule{
				{Name: "keyword.operator.assignment.masterbelt", Match: `=`},
				{Name: "keyword.operator.masterbelt", Match: `:`},
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
	autoClosing := []pair{
		{Open: token.BlockCommentOpen, Close: token.BlockCommentClose},
		{Open: `"`, Close: `"`},
	}
	surrounding := []pair{{Open: `"`, Close: `"`}}
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
