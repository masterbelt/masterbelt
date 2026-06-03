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

	// Operators and punctuation.
	Colon  // :
	Assign // =

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
	Colon:        "Colon",
	Assign:       "Assign",
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

// keywords maps reserved identifiers to their keyword Kind.
var keywords = map[string]Kind{
	"const": Const,
	"pub":   Pub,
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
