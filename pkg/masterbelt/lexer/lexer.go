// Package lexer turns masterbelt source into a stream of tokens.
//
// The package is organised as:
//
//	lexer.go     the Lexer driver: New, Next, Tokens, Diagnostics
//	scan.go      the per-kind recognizers and diagnostic reporting
//	document.go  the incremental Document (relex on edit)
package lexer

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Lexer scans a source.File and produces tokens one at a time.
//
// The lexer is lossless: every byte is covered by exactly one token. Whitespace
// runs (Whitespace), newlines (Newline), and comments are emitted as tokens
// rather than discarded, so concatenating the token texts reproduces the source
// exactly — which formatters, LSP servers, and faithful round-tripping need. A
// parser simply skips the trivia kinds it does not care about.
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
//
//nolint:gocyclo,funlen // byte-dispatch lexer: one case per token kind, every case delegating to its scanner (the sanctioned flat-dispatch exception)
func (l *Lexer) Next() token.Token {
	start := l.offset
	if l.offset >= len(l.src) {
		return l.token(token.EOF, start)
	}

	switch c := l.src[l.offset]; {
	case c == ' ' || c == '\t' || c == '\r':
		return l.scanWhitespace(start)
	case c == '\n':
		l.offset++
		return l.token(token.Newline, start)
	case c == '/':
		return l.scanSlash(start)
	case c == '"':
		return l.scanString(start)
	case c == ':':
		return l.scanFixed(start, 1, token.Colon)
	case c == '?':
		return l.scanFixed(start, 1, token.Question)
	case c == '(':
		return l.scanFixed(start, 1, token.LParen)
	case c == ')':
		return l.scanFixed(start, 1, token.RParen)
	case c == '{':
		return l.scanFixed(start, 1, token.LBrace)
	case c == '}':
		return l.scanFixed(start, 1, token.RBrace)
	case c == '[':
		return l.scanFixed(start, 1, token.LBracket)
	case c == ']':
		return l.scanFixed(start, 1, token.RBracket)
	case c == ',':
		return l.scanFixed(start, 1, token.Comma)
	case c == '.': // "..." (half-open range), ".." (closed range), or "." (member)
		return l.scanDot(start)
	case c == '+':
		return l.scanFixed(start, 1, token.Plus)
	case c == '-': // "->" or "-"
		return l.scanFixed2(start, '>', token.Arrow, token.Minus)
	case c == '*':
		return l.scanFixed(start, 1, token.Star)
	case c == '%':
		return l.scanFixed(start, 1, token.Percent)
	case c == '=': // "=" or "=="
		return l.scanFixed2(start, '=', token.EqEq, token.Assign)
	case c == '!': // "!" or "!="
		return l.scanFixed2(start, '=', token.BangEq, token.Bang)
	case c == '<': // "<" or "<="
		return l.scanFixed2(start, '=', token.LtEq, token.Lt)
	case c == '>': // ">" or ">="
		return l.scanFixed2(start, '=', token.GtEq, token.Gt)
	case c == '&': // "&&" only; a lone "&" begins no token
		if l.peek(1) == '&' {
			return l.scanFixed(start, 2, token.AmpAmp)
		}
		return l.scanIllegal(start)
	case c == '|': // "||" (logical or) or a lone "|" (union)
		if l.peek(1) == '|' {
			return l.scanFixed(start, 2, token.PipePipe)
		}
		return l.scanFixed(start, 1, token.Pipe)
	case isLetter(c):
		// A D opening an ISO date shape is a datetime literal; any other D
		// run — D2009 without its date, Dragon — is an identifier.
		if c == 'D' {
			if tok, ok := l.scanDatetime(start); ok {
				return tok
			}
		}
		return l.scanIdent(start)
	case isDigit(c):
		return l.scanNumber(start)
	default:
		return l.scanIllegal(start)
	}
}

// peek returns the byte n positions past the cursor, or 0 if that is past the
// end of input. It lets the maximal-munch operators look one byte ahead.
func (l *Lexer) peek(n int) byte {
	if l.offset+n < len(l.src) {
		return l.src[l.offset+n]
	}
	return 0
}

// token builds a token of the given kind spanning [start, l.offset).
func (l *Lexer) token(kind token.Kind, start int) token.Token {
	return token.Token{
		Kind:   kind,
		Offset: start,
		Width:  l.offset - start,
	}
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
