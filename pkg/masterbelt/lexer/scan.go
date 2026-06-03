package lexer

import (
	"unicode/utf8"

	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
)

// scanWhitespace scans a run of spaces, tabs, and carriage returns. Newlines are
// their own token, so they terminate the run.
func (l *Lexer) scanWhitespace(start int) token.Token {
	for l.offset < len(l.src) {
		switch l.src[l.offset] {
		case ' ', '\t', '\r':
			l.offset++
		default:
			return l.token(token.Whitespace, start)
		}
	}
	return l.token(token.Whitespace, start)
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

func isLetter(c byte) bool {
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

func isIdentPart(c byte) bool {
	return isLetter(c) || isDigit(c)
}
