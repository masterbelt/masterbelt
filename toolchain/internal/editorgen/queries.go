package main

import (
	"fmt"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// queriesDir holds the generated tree-sitter highlight queries. The default
// queries/highlights.scm follows nvim-treesitter's capture names (the de facto
// standard, and what GitHub's tree-sitter path reads); editors whose capture
// vocabulary differs get a variant in queries/<editor>/highlights.scm. There is
// one source — the category table and the node->category rules below — and the
// generator absorbs the per-editor naming differences (C-2 plan §4).
const queriesDir = treeSitterDir + "/queries"

// Two highlight sub-categories the cold-start layer distinguishes that are not
// among semantic.go's 13 token types: a doc comment and a string escape. They
// ride alongside the canonical categories in the capture maps.
const (
	catCommentDoc   = "comment.doc"
	catStringEscape = "string.escape"
)

// tsTarget is a highlight target: an editor family and the capture name it uses
// for each canonical category (semantic.go's vocabulary, plus the two
// sub-categories above). Only the names differ between editors; the query
// structure — which node carries which category — is shared.
type tsTarget struct {
	// dir is the subdirectory under queries/ ("" is the default, nvim-flavored,
	// which tree-sitter.json points at).
	dir     string
	capture map[string]string // canonical category -> capture name, no leading '@'
}

// cap is the target's capture token (with the leading '@') for a category. A
// target's own map lists only the captures where it diverges from the default
// (nvim) vocabulary, so cap falls back to defaultCaptures, then to the category
// name itself.
func (t tsTarget) cap(category string) string {
	if c, ok := t.capture[category]; ok {
		return "@" + c
	}
	if c, ok := defaultCaptures[category]; ok {
		return "@" + c
	}
	return "@" + category
}

// defaultCaptures is the nvim-treesitter vocabulary — the base every target
// inherits (and what GitHub's tree-sitter path reads). A target overrides only
// where its convention differs (see tsTargets), so the shared names live here
// once.
var defaultCaptures = map[string]string{
	catKeyword:      "keyword",
	catComment:      "comment",
	catCommentDoc:   "comment.documentation",
	catType:         "type",
	catVariable:     "variable",
	catNumber:       "number",
	catString:       "string",
	catStringEscape: "string.escape",
	catOperator:     "operator",
	catNamespace:    "module",
	catProperty:     "property",
	catMethod:       "function.method",
	catParameter:    "variable.parameter",
	catFunction:     "function",
	catEnumMember:   "constant",
}

// tsTargets are the highlight targets generated. The default (nvim-treesitter)
// carries no overrides; helix and zed list only the captures whose names differ
// from the default — the differences the generator exists to absorb (C-2 §4).
var tsTargets = []tsTarget{
	{dir: ""}, // nvim-treesitter (and GitHub): the default vocabulary
	{dir: "helix", capture: map[string]string{
		catCommentDoc:   "comment.block.documentation",
		catNumber:       "constant.numeric.integer",
		catStringEscape: "constant.character.escape",
		catNamespace:    "namespace",
		catProperty:     "variable.other.member",
	}},
	{dir: "zed", capture: map[string]string{
		catCommentDoc: "comment.doc",
		catNamespace:  "namespace",
	}},
}

// semanticOperatorSpellings is the operator set the language server colours as
// `operator` (semantic.go's classifyToken): the type/assignment colon and
// equals, the arrow, the ternary question mark, and the two range operators.
// The arithmetic and comparison operators are deliberately left uncoloured at
// cold start, exactly as the server and the TextMate grammar leave them.
func semanticOperatorSpellings() []string {
	return []string{
		token.Colon.Symbol(), token.Assign.Symbol(), token.Arrow.Symbol(),
		token.Question.Symbol(), token.DotDot.Symbol(), token.DotDotDot.Symbol(),
	}
}

// contextModifiers are contextual accessor/static modifiers: `get`, `set`, and
// `static`. They are intentionally not reserved words in token.Keywords because
// they should remain legal identifiers outside modifier positions; the lexer
// therefore emits them as identifiers. During highlighting, when they appear in
// modifier/accessor context, we promote them to keyword colouring (keyword.control),
// matching semantic.go's Modifier handling and the TextMate #modifiers rule.
var contextModifiers = []string{"get", "set", "static"}

// highlightRule maps a tree-sitter query pattern, with a single %s capture
// placeholder, to the category whose capture name fills it.
type highlightRule struct {
	pattern  string
	category string
}

// declRules colour declared names; the kind of declaration fixes the colour,
// matching semantic.go's identClasses.
var declRules = []highlightRule{
	{pattern: "(const_decl name: (identifier) %s)\n", category: catVariable},
	{pattern: "(let_stmt name: (identifier) %s)\n", category: catVariable},
	{pattern: "(type_decl name: (identifier) %s)\n", category: catType},
	{pattern: "(enum_decl name: (identifier) %s)\n", category: catType},
	{pattern: "(interface_decl name: (identifier) %s)\n", category: catType},
	{pattern: "(master_decl name: (identifier) %s)\n", category: catType},
	{pattern: "(generic_param name: (identifier) %s)\n", category: catType},
	{pattern: "(func_decl name: (identifier) %s)\n", category: catFunction},
	{pattern: "(method_decl name: (identifier) %s)\n", category: catMethod},
	{pattern: "(interface_member name: (identifier) %s)\n", category: catMethod},
	{pattern: "(param name: (identifier) %s)\n", category: catParameter},
	{pattern: "(field name: (identifier) %s)\n", category: catProperty},
	{pattern: "(record_field name: (identifier) %s)\n", category: catProperty},
	{pattern: "(master_primary (identifier) %s)\n", category: catProperty},
	{pattern: "(enum_member name: (identifier) %s)\n", category: catEnumMember},
	{pattern: "(use_decl (identifier) %s)\n", category: catNamespace},
	{pattern: "(modifier) %s\n", category: catKeyword},
	{pattern: "(master_keyword) %s\n", category: catKeyword},
}

// refRules colour name references. A name in a type position is a type; the
// type prefix of a record literal is too. A bare value reference is a
// variable, and a member access reads as a property.
var refRules = []highlightRule{
	{pattern: "(type_name (identifier) %s)\n", category: catType},
	{pattern: "(record_literal type: (identifier) %s)\n", category: catType},
	{pattern: "(value_ref (identifier) %s)\n", category: catVariable},
	{pattern: "(member_expr member: (identifier) %s)\n", category: catProperty},
}

// overrideRules are contextual overrides emitted last, so they win: a call's
// callee names the function or method being called, not a plain value or
// property.
var overrideRules = []highlightRule{
	{pattern: "(call_expr callee: (value_ref (identifier) %s))\n", category: catFunction},
	{pattern: "(call_expr callee: (member_expr member: (identifier) %s))\n", category: catMethod},
}

// keywordAlternation is the tree-sitter node alternation matching any reserved
// word used as a bare token — ["assert" "async" …] — for the name-position rules
// below. The grammar reads a reserved word as a name (the _name rule) in member,
// projection, field, and parameter positions, where it appears as the bare
// keyword token rather than an identifier node; these rules recolour it.
func keywordAlternation() string {
	quoted := make([]string, 0, len(token.Keywords()))
	for _, kw := range token.Keywords() {
		quoted = append(quoted, fmt.Sprintf("%q", kw))
	}
	return "[" + strings.Join(quoted, " ") + "]"
}

// nameKeywordRules colour a reserved word used as a name as that name's role
// rather than as the keyword: a member after "." is a property, a type-position
// projection a type, a record field or record-literal field a property, a
// parameter a parameter, and a method declaration's name a method — the _name
// positions of the grammar. They are emitted after the keyword block so the role
// colour wins (later patterns win in tree-sitter), the query twin of semantic.go
// classifying a keyword in a name position by its parent.
func nameKeywordRules(kw string) []highlightRule {
	return []highlightRule{
		{pattern: "(member_expr member: " + kw + " %s)\n", category: catProperty},
		{pattern: "(type_name " + kw + " %s)\n", category: catType},
		{pattern: "(field name: " + kw + " %s)\n", category: catProperty},
		{pattern: "(record_field name: " + kw + " %s)\n", category: catProperty},
		{pattern: "(param name: " + kw + " %s)\n", category: catParameter},
		{pattern: "(method_decl name: " + kw + " %s)\n", category: catMethod},
	}
}

// keywordCalleeRules override a keyword-named member used as a call callee to a
// method colour, emitted last so it wins over the property colour the
// name-position member rule gives it — the keyword twin of overrideRules.
func keywordCalleeRules(kw string) []highlightRule {
	return []highlightRule{
		{pattern: "(call_expr callee: (member_expr member: " + kw + " %s))\n", category: catMethod},
	}
}

// writeRules renders each rule's pattern with the target's capture name for
// the rule's category.
func writeRules(b *strings.Builder, t tsTarget, rules []highlightRule) {
	for _, r := range rules {
		fmt.Fprintf(b, r.pattern, t.cap(r.category))
	}
}

// buildHighlights renders one target's highlights.scm. The node->category
// structure is identical across targets; t.cap projects each category to the
// target's capture name. Later patterns win in tree-sitter highlighting, so the
// contextual overrides (a call's callee is a function, not a plain variable)
// follow the general identifier rules.
func buildHighlights(t tsTarget) string {
	var b strings.Builder
	fmt.Fprintf(&b, `; Code generated by editorgen; DO NOT EDIT.
;
; tree-sitter highlight queries for masterbelt, projected from the category
; table (the language server's semantic token types) to %s's capture names.
; The query structure is shared across editors; only the capture names differ.

`, targetLabel(t))

	// Comments. A doc comment gets its own capture; the line and block forms
	// share the plain comment capture.
	fmt.Fprintf(&b, "(doc_comment) %s\n", t.cap(catCommentDoc))
	fmt.Fprintf(&b, "[(line_comment) (block_comment)] %s\n\n", t.cap(catComment))

	// Literals. datetime and duration colour as numbers, as in the cold-start
	// TextMate grammar; the escape sits inside the string.
	fmt.Fprintf(&b, "[(integer) (datetime) (duration)] %s\n", t.cap(catNumber))
	fmt.Fprintf(&b, "(escape_sequence) %s\n", t.cap(catStringEscape))
	fmt.Fprintf(&b, "(string) %s\n\n", t.cap(catString))

	// Keywords — the reserved words plus the accessor/static context keywords,
	// uniformly (true/false/null and the effect words are reserved, so they are
	// in the list), matching semantic.go's uniform keyword classification.
	b.WriteString("[\n")
	for _, kw := range token.Keywords() {
		fmt.Fprintf(&b, "  %q\n", kw)
	}
	for _, m := range contextModifiers {
		fmt.Fprintf(&b, "  %q\n", m)
	}
	fmt.Fprintf(&b, "] %s\n\n", t.cap(catKeyword))

	// Operators — the server's operator set.
	b.WriteString("[\n")
	for _, sym := range semanticOperatorSpellings() {
		fmt.Fprintf(&b, "  %q\n", sym)
	}
	fmt.Fprintf(&b, "] %s\n\n", t.cap(catOperator))

	// Declared names, then references, then the keyword-in-name-position rules,
	// then the contextual overrides; later patterns win, so a reserved word used
	// as a name takes its role colour over the keyword block above it, and a call
	// callee takes the function/method colour over that. The blank lines between
	// groups match the original hand-written layout.
	kw := keywordAlternation()
	writeRules(&b, t, declRules)
	b.WriteString("\n")
	writeRules(&b, t, refRules)
	writeRules(&b, t, nameKeywordRules(kw))
	b.WriteString("\n")
	writeRules(&b, t, overrideRules)
	writeRules(&b, t, keywordCalleeRules(kw))

	return b.String()
}

// targetLabel names a target for the generated file's header comment.
func targetLabel(t tsTarget) string {
	if t.dir == "" {
		return "nvim-treesitter (the default)"
	}
	return t.dir
}

// highlightPath is the output path for a target's highlights.scm: the default
// target writes queries/highlights.scm, the rest queries/<dir>/highlights.scm.
func highlightPath(t tsTarget) string {
	if t.dir == "" {
		return queriesDir + "/highlights.scm"
	}
	return queriesDir + "/" + t.dir + "/highlights.scm"
}

// writeQueries emits every target's highlights.scm.
func writeQueries() error {
	for _, t := range tsTargets {
		if err := writeFile(highlightPath(t), buildHighlights(t)); err != nil {
			return err
		}
	}
	return nil
}
