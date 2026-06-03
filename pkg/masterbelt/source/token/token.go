// Package token defines the lexical tokens of the masterbelt language and how
// they map back onto their source location.
package token

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
)

// Kind identifies the lexical category of a Token.
type Kind int

const (
	Illegal Kind = iota // a byte (or rune) that begins no valid token
	EOF                 // end of input

	// Comments.
	BlockComment // /* ... */
	LineComment  // // ...
	DocComment   // /// ...

	// Identifiers and literals.
	Ident // identifier or type name, e.g. MaxLevel, int64
	Int   // integer literal, e.g. 100

	// Keywords.
	Const // const
	Pub   // pub
	True  // true
	False // false

	// Operators and punctuation.
	Colon    // :
	Assign   // =
	Plus     // +
	Minus    // -
	Star     // *
	Slash    // /
	Percent  // %
	EqEq     // ==
	BangEq   // !=
	Lt       // <
	LtEq     // <=
	Gt       // >
	GtEq     // >=
	AmpAmp   // &&
	PipePipe // ||
	Bang     // !

	// Trivia. Emitted so the token stream covers every byte and can reproduce
	// the source exactly (needed by formatters and faithful round-tripping).
	Whitespace // a run of spaces, tabs, and carriage returns
	Newline    // \n
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
	Const:        "Const",
	Pub:          "Pub",
	True:         "True",
	False:        "False",
	Colon:        "Colon",
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

// spelling maps each fixed-spelling token — the operators and punctuation — to
// its source text. Variable-text kinds (Ident, Int, comments, trivia) and the
// keywords (whose spellings live in the keywords map) are absent. It is the
// source of truth for operator spellings, used to name them in diagnostics and
// available to tooling such as the editor grammar generator.
var spelling = map[Kind]string{
	Colon:    ":",
	Assign:   "=",
	Plus:     "+",
	Minus:    "-",
	Star:     "*",
	Slash:    "/",
	Percent:  "%",
	EqEq:     "==",
	BangEq:   "!=",
	Lt:       "<",
	LtEq:     "<=",
	Gt:       ">",
	GtEq:     ">=",
	AmpAmp:   "&&",
	PipePipe: "||",
	Bang:     "!",
}

// Symbol returns the source spelling of a fixed operator or punctuation kind, or
// "" for kinds whose text varies (identifiers, literals, comments) or that have
// no spelling.
func (k Kind) Symbol() string {
	return spelling[k]
}

// keywords maps reserved identifiers to their keyword Kind.
var keywords = map[string]Kind{
	"const": Const,
	"pub":   Pub,
	"true":  True,
	"false": False,
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
