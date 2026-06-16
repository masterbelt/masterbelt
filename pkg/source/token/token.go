// Package token defines the lexical tokens of the masterbelt language and how
// they map back onto their source location.
package token

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/masterbelt/masterbelt/pkg/source"
)

// Kind identifies the lexical category of a Token.
type Kind int

// The token kinds, one per lexical category; see each kind's comment.
const (
	Illegal Kind = iota // a byte (or rune) that begins no valid token
	EOF                 // end of input

	// Comments.

	BlockComment // /* ... */
	LineComment  // // ...
	DocComment   // /// ...

	// Identifiers and literals.

	Ident       // identifier or type name, e.g. MaxLevel, int64
	Int         // decimal integer literal, e.g. 100
	BinInt      // binary integer literal, e.g. 0b1010
	OctInt      // octal integer literal, e.g. 0o17
	HexInt      // hexadecimal integer literal, e.g. 0xFF
	String      // string literal, e.g. "label"
	DatetimeLit // datetime literal, e.g. D2009-03-31T23:59:59.000Z
	DurationLit // duration literal, e.g. 3w4d5h6m7s8ms

	// Keywords.

	Const     // const
	Pub       // pub
	True      // true
	False     // false
	Type      // type
	Enum      // enum
	Interface // interface
	Impl      // impl
	Fn        // fn
	Return    // return
	Self      // self
	Null      // null
	Extern    // extern
	Builtin   // builtin
	Use       // use
	From      // from
	Assert    // assert
	Where     // where
	Io        // io (effect: touches the world)
	Async     // async (effect: has suspension points)
	Nondet    // nondet (effect: does not reproduce)
	Await     // await (consumes async at a call site)
	Switch    // switch (value-dispatch control statement)
	If        // if (boolean control statement)
	Else      // else (the alternative branch of an if)
	Let       // let (mutable block-local binding)
	Match     // match (type-dispatch control statement)
	For       // for (collection-iteration control statement)
	Of        // of (for: bind the value — a list element, a map value)
	In        // in (for: bind the key — a map key, a list index)

	// Operators and punctuation.

	Colon     // :
	Question  // ?
	Assign    // =
	Plus      // +
	Minus     // -
	Star      // *
	Slash     // /
	Percent   // %
	EqEq      // ==
	BangEq    // !=
	Lt        // <
	LtEq      // <=
	Gt        // >
	GtEq      // >=
	AmpAmp    // &&
	PipePipe  // ||
	Bang      // !
	LParen    // (
	RParen    // )
	LBrace    // {
	RBrace    // }
	LBracket  // [
	RBracket  // ]
	Comma     // ,
	Dot       // .
	DotDot    // .. (the closed range operator: a..b is [min, max], both ends)
	DotDotDot // ... (the half-open range operator: a...b excludes the larger end)
	Pipe      // |
	Arrow     // ->

	// Trivia. Emitted so the token stream covers every byte and can reproduce
	// the source exactly (needed by formatters and faithful round-tripping).

	Whitespace // a run of spaces, tabs, and carriage returns
	Newline    // \n

	numKinds // sentinel: the count of Kind values; not a real kind
)

// firstKeyword and lastKeyword bound the contiguous run of keyword kinds in the
// const block, so a test can require every kind in the range to appear in the
// keywords map (and nothing outside it to). They must stay pinned to the first
// and last keyword declared above.
const (
	firstKeyword = Const
	lastKeyword  = In
)

// firstOperator and lastOperator bound the contiguous run of operator and
// punctuation kinds, so a test can require every kind in the range to have a
// spelling entry. They must stay pinned to the first and last operator above.
const (
	firstOperator = Colon
	lastOperator  = Arrow
)

// kindNames maps each Kind to its name, indexed by Kind value.
var kindNames = [...]string{
	Illegal:      "Illegal",
	EOF:          "EOF",
	BlockComment: "BlockComment",
	LineComment:  "LineComment",
	DocComment:   "DocComment",
	Ident:        "Ident",
	Int:          "Int",
	BinInt:       "BinInt",
	OctInt:       "OctInt",
	HexInt:       "HexInt",
	String:       "String",
	DatetimeLit:  "DatetimeLit",
	DurationLit:  "DurationLit",
	Const:        "Const",
	Pub:          "Pub",
	True:         "True",
	False:        "False",
	Type:         "Type",
	Enum:         "Enum",
	Interface:    "Interface",
	Impl:         "Impl",
	Fn:           "Fn",
	Return:       "Return",
	Self:         "Self",
	Null:         "Null",
	Extern:       "Extern",
	Builtin:      "Builtin",
	Use:          "Use",
	From:         "From",
	Assert:       "Assert",
	Where:        "Where",
	Io:           "Io",
	Async:        "Async",
	Nondet:       "Nondet",
	Await:        "Await",
	Switch:       "Switch",
	If:           "If",
	Else:         "Else",
	Let:          "Let",
	Match:        "Match",
	For:          "For",
	Of:           "Of",
	In:           "In",
	Colon:        "Colon",
	Question:     "Question",
	Assign:       "Assign",
	Plus:         "Plus",
	Minus:        "Minus",
	Star:         "Star",
	Slash:        "Slash",
	Percent:      "Percent",
	EqEq:         "EqEq",
	BangEq:       "BangEq",
	Lt:           "Lt",
	LtEq:         "LtEq",
	Gt:           "Gt",
	GtEq:         "GtEq",
	AmpAmp:       "AmpAmp",
	PipePipe:     "PipePipe",
	Bang:         "Bang",
	LParen:       "LParen",
	RParen:       "RParen",
	LBrace:       "LBrace",
	RBrace:       "RBrace",
	LBracket:     "LBracket",
	RBracket:     "RBracket",
	Comma:        "Comma",
	Dot:          "Dot",
	DotDot:       "DotDot",
	DotDotDot:    "DotDotDot",
	Pipe:         "Pipe",
	Arrow:        "Arrow",
	Whitespace:   "Whitespace",
	Newline:      "Newline",
}

// String returns the name of the kind, for debugging and diagnostics.
func (k Kind) String() string {
	if 0 <= int(k) && int(k) < len(kindNames) && kindNames[k] != "" {
		return kindNames[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// kindByName is the reverse of the kindNames table, built once at init.
var kindByName = func() map[string]Kind {
	m := make(map[string]Kind, numKinds)
	for k := range numKinds {
		m[k.String()] = k
	}
	return m
}()

// ParseKind returns the Kind named name — the inverse of Kind.String — and
// whether the name is a known kind. It is what reads token kinds back from
// the CST's text representation.
func ParseKind(name string) (Kind, bool) {
	k, ok := kindByName[name]
	return k, ok
}

// spelling maps each fixed-spelling token — the operators and punctuation — to
// its source text. Variable-text kinds (Ident, Int, comments, trivia) and the
// keywords (whose spellings live in the keywords map) are absent. It is the
// source of truth for operator spellings, used to name them in diagnostics and
// available to tooling such as the editor grammar generator.
var spelling = map[Kind]string{
	Colon:     ":",
	Question:  "?",
	Assign:    "=",
	Plus:      "+",
	Minus:     "-",
	Star:      "*",
	Slash:     "/",
	Percent:   "%",
	EqEq:      "==",
	BangEq:    "!=",
	Lt:        "<",
	LtEq:      "<=",
	Gt:        ">",
	GtEq:      ">=",
	AmpAmp:    "&&",
	PipePipe:  "||",
	Bang:      "!",
	LParen:    "(",
	RParen:    ")",
	LBrace:    "{",
	RBrace:    "}",
	LBracket:  "[",
	RBracket:  "]",
	Comma:     ",",
	Dot:       ".",
	DotDot:    "..",
	DotDotDot: "...",
	Pipe:      "|",
	Arrow:     "->",
}

// Symbol returns the source spelling of a fixed operator or punctuation kind, or
// "" for kinds whose text varies (identifiers, literals, comments) or that have
// no spelling.
func (k Kind) Symbol() string {
	return spelling[k]
}

// keywords maps reserved identifiers to their keyword Kind.
var keywords = map[string]Kind{
	"const":     Const,
	"pub":       Pub,
	"true":      True,
	"false":     False,
	"type":      Type,
	"enum":      Enum,
	"interface": Interface,
	"impl":      Impl,
	"fn":        Fn,
	"return":    Return,
	"self":      Self,
	"null":      Null,
	"extern":    Extern,
	"builtin":   Builtin,
	"use":       Use,
	"from":      From,
	"assert":    Assert,
	"where":     Where,
	"io":        Io,
	"async":     Async,
	"nondet":    Nondet,
	"await":     Await,
	"switch":    Switch,
	"if":        If,
	"else":      Else,
	"let":       Let,
	"match":     Match,
	"for":       For,
	"of":        Of,
	"in":        In,
}

// Effect reports whether the kind is an effect keyword — the declarations a
// function signature may carry between fn and its name.
func (k Kind) Effect() bool {
	return k == Io || k == Async || k == Nondet
}

// MethodMarker reports whether the keyword is a declaration marker that precedes a
// method's name — pub, extern, fn, or an effect — rather than a name itself. A
// method name may be any other reserved word (fn where(...)), but not one of these:
// the grammar consumes them structurally before the name, so admitting one there
// would shadow the marker and leave the method unnamed.
func (k Kind) MethodMarker() bool {
	switch k {
	case Pub, Extern, Fn, Io, Async, Nondet:
		return true
	default:
		return false
	}
}

// Keyword reports whether the kind is one of the reserved-word keyword kinds
// (the contiguous Const..In run). It is the predicate that lets a keyword be
// read as a plain identifier where the grammar makes one unambiguous — a member
// name after ".", a record field name, a function parameter name — so type, for,
// and the rest stay usable as ordinary names (item.type, fn f(for: int)) without
// the lexer carving out per-word exceptions.
func (k Kind) Keyword() bool {
	return firstKeyword <= k && k <= lastKeyword
}

// Lookup returns the keyword Kind for ident, or Ident if it is not a
// reserved word.
func Lookup(ident string) Kind {
	if k, ok := keywords[ident]; ok {
		return k
	}
	return Ident
}

// Keywords returns the reserved words in sorted order. It is the single source
// of truth for the language's keywords: the lexer matches against it (via
// Lookup), and external tooling — such as the editor grammar generator — reads
// it so the syntax highlighting never has to be maintained separately.
func Keywords() []string {
	out := make([]string, 0, len(keywords))
	for kw := range keywords {
		out = append(out, kw)
	}
	sort.Strings(out)
	return out
}

// OperatorSpelling pairs an operator or punctuation kind's name with its source
// spelling. Operators returns these.
type OperatorSpelling struct {
	Name   string // the Kind's name, e.g. "Arrow"
	Symbol string // its source spelling, e.g. "->"
}

// Operators returns every fixed-spelling operator and punctuation kind, in Kind
// declaration order, paired with its spelling. It is the operator counterpart
// of Keywords: the single source the editor grammar generators read so their
// operator lexemes — the TextMate scopes and the tree-sitter tokens — never
// drift from the lexer's spelling map.
func Operators() []OperatorSpelling {
	out := make([]OperatorSpelling, 0, lastOperator-firstOperator+1)
	for k := firstOperator; k <= lastOperator; k++ {
		out = append(out, OperatorSpelling{Name: k.String(), Symbol: k.Symbol()})
	}
	return out
}

// Comment markers are the language's comment syntax. The lexer scans them and
// the editor-config generator emits its grammar and language configuration from
// them, so syntax highlighting and comment toggling never drift from the lexer.
// A lexer test (TestCommentMarkersMatchLexer) pins these to the scanner's actual
// behaviour.
const (
	LineCommentPrefix = "//"
	DocCommentPrefix  = "///"
	BlockCommentOpen  = "/*"
	BlockCommentClose = "*/"
)

// Token is a single lexical token. It stores only its byte range within the
// file — Kind, the start Offset, and the byte Width — and never the absolute
// line/column position. Keeping tokens width-based means an edit shifts at
// most their Offset: unchanged tokens can be reused without recomputing
// positions, which is the property an incremental parser relies on.
//
// Resolve the token's text and source span on demand with Text and Span,
// passing the File the token was lexed from.
type Token struct {
	Kind   Kind
	Offset int // byte offset of the token start within the file
	Width  int // byte length of the token
}

// End returns the byte offset one past the token (Offset + Width).
func (t Token) End() int {
	return t.Offset + t.Width
}

// Text returns the source text the token covers, read from buf (the File or
// Text the token was lexed from).
func (t Token) Text(buf source.Buffer) string {
	return string(buf.Slice(t.Offset, t.End()))
}

// Span resolves the token's source span, computing line/column positions from
// buf on demand.
func (t Token) Span(buf source.Buffer) source.Span {
	return buf.Span(t.Offset, t.End())
}

// String renders the token as Kind@offset+width, e.g. Const@30+5. It needs no
// File because the literal text is not stored on the token.
func (t Token) String() string {
	return fmt.Sprintf("%s@%d+%d", t.Kind, t.Offset, t.Width)
}
