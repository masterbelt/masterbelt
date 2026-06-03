// Command editorgen generates the VS Code editor configuration for masterbelt —
// the TextMate grammar and the language-configuration — from the lexer's own
// definitions (the keyword table and comment markers in package token), so none
// of it is maintained by hand.
//
// The grammar is only a "cold start" approximation (the lexical basics that
// colour a file before the language server responds); the accurate highlighting
// comes from the server's semantic tokens, which read the same parse. The
// language-configuration's comment settings are derived from the same comment
// markers the lexer scans.
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

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
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
	Include string `json:"include,omitempty"`
	Name    string `json:"name,omitempty"`
	Match   string `json:"match,omitempty"`
	Begin   string `json:"begin,omitempty"`
	End     string `json:"end,omitempty"`
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
			{Include: "#operators"},
			{Include: "#identifiers"},
		},
		Repository: map[string]ruleGroup{
			"comments": {Patterns: []rule{
				{Name: "comment.line.documentation.masterbelt", Match: regexp.QuoteMeta(token.DocCommentPrefix) + `.*$`},
				{Name: "comment.line.double-slash.masterbelt", Match: regexp.QuoteMeta(token.LineCommentPrefix) + `.*$`},
				{Name: "comment.block.masterbelt", Begin: regexp.QuoteMeta(token.BlockCommentOpen), End: regexp.QuoteMeta(token.BlockCommentClose)},
			}},
			"keywords": {Patterns: []rule{
				{Name: "keyword.other.masterbelt", Match: keywordPattern()},
			}},
			"numbers": {Patterns: []rule{
				{Name: "constant.numeric.integer.masterbelt", Match: `\b[0-9]+\b`},
			}},
			"operators": {Patterns: []rule{
				{Name: "keyword.operator.assignment.masterbelt", Match: `=`},
				{Name: "punctuation.separator.type.masterbelt", Match: `:`},
			}},
			"identifiers": {Patterns: []rule{
				{Name: "variable.other.masterbelt", Match: `\b[A-Za-z_][A-Za-z0-9_]*\b`},
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
	return languageConfig{
		Comments: commentConfig{
			LineComment:  token.LineCommentPrefix,
			BlockComment: [2]string{token.BlockCommentOpen, token.BlockCommentClose},
		},
		// The language has no brackets yet; block comments auto-close.
		Brackets:         [][]string{},
		AutoClosingPairs: []pair{{Open: token.BlockCommentOpen, Close: token.BlockCommentClose}},
		SurroundingPairs: []pair{},
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
