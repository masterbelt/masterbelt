// Package lexer turns masterbelt source into a stream of tokens.
package lexer

import (
	"unicode/utf8"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
)

// Lexer scans a source.File and produces tokens one at a time.
//
// The lexer is faithful: comments and newlines are emitted as tokens rather
// than discarded, so the stream can be reproduced or consumed by a parser
// that treats newlines as statement terminators. Only spaces, tabs, and
// carriage returns are skipped.
//
// Malformed input never aborts scanning. Problems are recorded as diagnostics
// (retrievable with Diagnostics) while the lexer keeps producing the most
// plausible tokens, so an editor or incremental parser can keep working on a
// half-typed buffer.
type Lexer struct {
	src    []byte
	offset int // offset of the next unread byte
	diags  *diagnostic.List
}

// New creates a Lexer over file.
func New(file *source.File) *Lexer {
	return &Lexer{
		src:   file.Source(),
		diags: &diagnostic.List{},
	}
}

// Diagnostics returns the problems found so far. Call it after draining the
// token stream (Tokens, or Next until EOF) to get the complete set.
func (l *Lexer) Diagnostics() []diagnostic.Diagnostic {
	return l.diags.Items()
}

// Tokens scans the entire input and returns every token, terminated by a
// single EOF token.
func (l *Lexer) Tokens() []token.Token {
	var tokens []token.Token
	for {
		tok := l.Next()
		tokens = append(tokens, tok)
		if tok.Kind == token.EOF {
			return tokens
		}
	}
}

// Next scans and returns the next token. Once the input is exhausted it
// returns an EOF token on every call.
func (l *Lexer) Next() token.Token {
	l.skipSpaces()

	start := l.offset
	if l.offset >= len(l.src) {
		return l.token(token.EOF, start)
	}

	switch c := l.src[l.offset]; {
	case c == '\n':
		l.offset++
		return l.token(token.Newline, start)
	case c == '/':
		return l.scanSlash(start)
	case c == ':':
		l.offset++
		return l.token(token.Colon, start)
	case c == '=':
		l.offset++
		return l.token(token.Assign, start)
	case isLetter(c):
		return l.scanIdent(start)
	case isDigit(c):
		return l.scanInt(start)
	default:
		return l.scanIllegal(start)
	}
}

// skipSpaces advances past spaces, tabs, and carriage returns. Newlines are
// left for Next to emit as tokens.
func (l *Lexer) skipSpaces() {
	for l.offset < len(l.src) {
		switch l.src[l.offset] {
		case ' ', '\t', '\r':
			l.offset++
		default:
			return
		}
	}
}

// scanSlash dispatches the three comment forms (//, ///, /* */). A lone '/'
// begins no token in this language and is reported as Illegal.
func (l *Lexer) scanSlash(start int) token.Token {
	if l.offset+1 < len(l.src) {
		switch l.src[l.offset+1] {
		case '/':
			return l.scanLineComment(start)
		case '*':
			return l.scanBlockComment(start)
		}
	}
	l.offset++
	l.reportUnexpectedChar(start, l.offset, '/')
	return l.token(token.Illegal, start)
}

// scanLineComment scans from "//" to the end of the line (the newline is left
// for Next). A comment opening with "///" is a DocComment.
func (l *Lexer) scanLineComment(start int) token.Token {
	kind := token.LineComment
	if l.offset+2 < len(l.src) && l.src[l.offset+2] == '/' {
		kind = token.DocComment
	}
	for l.offset < len(l.src) && l.src[l.offset] != '\n' {
		l.offset++
	}
	return l.token(kind, start)
}

// scanBlockComment scans a /* ... */ comment. A comment left unterminated at
// end of input is reported as an error but still returned as a BlockComment,
// so syntax highlighting stays stable while the closing */ is being typed.
func (l *Lexer) scanBlockComment(start int) token.Token {
	l.offset += 2 // consume "/*"
	for l.offset < len(l.src) {
		if l.src[l.offset] == '*' && l.offset+1 < len(l.src) && l.src[l.offset+1] == '/' {
			l.offset += 2 // consume "*/"
			return l.token(token.BlockComment, start)
		}
		l.offset++
	}
	l.reportUnterminatedBlockComment(start, l.offset)
	return l.token(token.BlockComment, start)
}

// scanIdent scans an identifier and resolves it to a keyword Kind when it
// matches a reserved word.
func (l *Lexer) scanIdent(start int) token.Token {
	for l.offset < len(l.src) && isIdentPart(l.src[l.offset]) {
		l.offset++
	}
	return l.token(token.Lookup(string(l.src[start:l.offset])), start)
}

// scanInt scans a run of decimal digits.
func (l *Lexer) scanInt(start int) token.Token {
	for l.offset < len(l.src) && isDigit(l.src[l.offset]) {
		l.offset++
	}
	return l.token(token.Int, start)
}

// scanIllegal consumes one whole rune that begins no valid token, reports it,
// and returns it as Illegal. Advancing by a full rune (rather than a byte)
// keeps a stray multibyte character from fragmenting into several tokens.
func (l *Lexer) scanIllegal(start int) token.Token {
	r, size := utf8.DecodeRune(l.src[l.offset:])
	l.offset += size
	l.reportUnexpectedChar(start, l.offset, r)
	return l.token(token.Illegal, start)
}

// token builds a token of the given kind spanning [start, l.offset).
func (l *Lexer) token(kind token.Kind, start int) token.Token {
	return token.Token{
		Kind:   kind,
		Offset: start,
		Width:  l.offset - start,
	}
}

// reportUnexpectedChar records an "unexpected character" diagnostic for the
// rune in [start, end). Diagnostics carry only byte offsets, so reporting needs
// no file and works the same whether lexing a whole file or a detached window.
func (l *Lexer) reportUnexpectedChar(start, end int, char rune) {
	l.diags.Add(newUnexpectedCharacterDiagnostic(start, end-start, char))
}

// reportUnterminatedBlockComment records an "unterminated block comment"
// diagnostic for the comment in [start, end).
func (l *Lexer) reportUnterminatedBlockComment(start, end int) {
	l.diags.Add(newUnterminatedBlockCommentDiagnostic(start, end-start))
}

// lex scans src in isolation and returns its tokens (offsets relative to src,
// excluding the trailing EOF) together with any diagnostics. The incremental
// relexer uses it to re-scan a detached window of bytes; because tokens and
// diagnostics are both offset-based, the window's results can be shifted and
// spliced back into the document.
func lex(src []byte) ([]token.Token, []diagnostic.Diagnostic) {
	l := &Lexer{src: src, diags: &diagnostic.List{}}
	var tokens []token.Token
	for {
		tok := l.Next()
		if tok.Kind == token.EOF {
			return tokens, l.diags.Items()
		}
		tokens = append(tokens, tok)
	}
}

func isLetter(c byte) bool {
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

func isIdentPart(c byte) bool {
	return isLetter(c) || isDigit(c)
}
