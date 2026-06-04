package lexer

import (
	"strconv"
	"unicode/utf8"

	"github.com/masterbelt/masterbelt/pkg/source/token"
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

// scanFixed emits a fixed-width operator or punctuation token of width bytes
// (1 or 2) starting at start.
func (l *Lexer) scanFixed(start, width int, kind token.Kind) token.Token {
	l.offset += width
	return l.token(kind, start)
}

// scanFixed2 emits a two-byte operator (kind two) when the byte after the
// cursor is next, and the one-byte operator (kind one) otherwise. It is the
// maximal-munch rule shared by "==", "!=", "<=", and ">=".
func (l *Lexer) scanFixed2(start int, next byte, two, one token.Kind) token.Token {
	if l.peek(1) == next {
		return l.scanFixed(start, 2, two)
	}
	return l.scanFixed(start, 1, one)
}

// scanSlash dispatches the comment forms (//, ///, /* */). A '/' that opens no
// comment is the division operator.
func (l *Lexer) scanSlash(start int) token.Token {
	if l.offset+1 < len(l.src) {
		switch l.src[l.offset+1] {
		case '/':
			return l.scanLineComment(start)
		case '*':
			return l.scanBlockComment(start)
		}
	}
	return l.scanFixed(start, 1, token.Slash)
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

// scanString scans a double-quoted string literal. Backslash introduces an
// escape sequence (scanEscape), so an escaped quote (\") does not close the
// string. Raw multi-byte UTF-8 is carried through verbatim. A string left
// unterminated at a newline or end of input is reported as an error but still
// returned as a String token, so highlighting stays stable while the closing
// quote is being typed (mirroring the unterminated block comment). A string
// does not span lines: a newline ends it.
func (l *Lexer) scanString(start int) token.Token {
	l.offset++ // consume the opening quote
	for l.offset < len(l.src) {
		switch l.src[l.offset] {
		case '\\':
			l.scanEscape()
		case '"':
			l.offset++ // consume the closing quote
			return l.token(token.String, start)
		case '\n':
			l.reportUnterminatedString(start, l.offset)
			return l.token(token.String, start)
		default:
			l.offset++
		}
	}
	l.reportUnterminatedString(start, l.offset)
	return l.token(token.String, start)
}

// simpleEscapes is the set of single-character escapes a string recognizes,
// beyond the \u{...} unicode escape: \n \r \t \0 \\ \".
var simpleEscapes = map[byte]bool{
	'n': true, 'r': true, 't': true, '0': true, '\\': true, '"': true,
}

// scanEscape consumes a backslash escape inside a string, reporting it when it
// is not a recognized sequence. The cursor sits on the backslash; on return it
// sits just past the escape and never past a newline or the end of input, so the
// caller can still detect an unterminated string. The decoded value is the
// semantic layer's concern; the lexer only validates the spelling.
func (l *Lexer) scanEscape() {
	escStart := l.offset
	l.offset++ // consume the backslash

	// A backslash at a newline or end of input is left for the unterminated
	// string report; it escapes nothing.
	if l.offset >= len(l.src) || l.src[l.offset] == '\n' {
		return
	}

	switch c := l.src[l.offset]; {
	case simpleEscapes[c]:
		l.offset++
	case c == 'u':
		l.offset++
		l.scanUnicodeEscape(escStart)
	default:
		// An unknown escape: consume the whole escaped rune (so a multi-byte
		// character is not split) and report the sequence.
		_, size := utf8.DecodeRune(l.src[l.offset:])
		l.offset += size
		l.reportInvalidEscape(escStart, l.offset)
	}
}

// scanUnicodeEscape validates a \u{...} unicode escape: "{", one to six hex
// digits naming a Unicode scalar value (at most 0x10FFFF, never a surrogate),
// then "}". The cursor sits just past the "u"; escStart is the backslash, used
// to anchor a diagnostic over the whole escape. A malformed or out-of-range
// escape is reported. Neither a newline nor the closing quote is consumed, so an
// unterminated string is still detected by the caller.
func (l *Lexer) scanUnicodeEscape(escStart int) {
	if l.offset >= len(l.src) || l.src[l.offset] != '{' {
		l.reportInvalidUnicodeEscape(escStart, l.offset)
		return
	}
	l.offset++ // consume "{"

	digitsStart := l.offset
	for l.offset < len(l.src) && isHexDigit(l.src[l.offset]) {
		l.offset++
	}
	digits := l.src[digitsStart:l.offset]

	if l.offset >= len(l.src) || l.src[l.offset] != '}' {
		l.reportInvalidUnicodeEscape(escStart, l.offset)
		return
	}
	l.offset++ // consume "}"

	if !validCodePoint(digits) {
		l.reportInvalidUnicodeEscape(escStart, l.offset)
	}
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

// reportUnterminatedString records an "unterminated string literal" diagnostic
// for the string in [start, end).
func (l *Lexer) reportUnterminatedString(start, end int) {
	l.diags.Add(newUnterminatedStringDiagnostic(start, end-start))
}

// reportInvalidEscape records an "invalid escape sequence" diagnostic for the
// escape in [start, end).
func (l *Lexer) reportInvalidEscape(start, end int) {
	l.diags.Add(newInvalidEscapeDiagnostic(start, end-start, string(l.src[start:end])))
}

// reportInvalidUnicodeEscape records an "invalid unicode escape" diagnostic for
// the escape in [start, end).
func (l *Lexer) reportInvalidUnicodeEscape(start, end int) {
	l.diags.Add(newInvalidUnicodeEscapeDiagnostic(start, end-start, string(l.src[start:end])))
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

func isHexDigit(c byte) bool {
	return isDigit(c) || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

// validCodePoint reports whether digits (the hex body of a \u{...} escape) names
// a Unicode scalar value: one to six hex digits, at most 0x10FFFF, and not a
// surrogate (the range reserved for UTF-16 pairs, which are not scalar values).
func validCodePoint(digits []byte) bool {
	if len(digits) < 1 || len(digits) > 6 {
		return false
	}
	v, err := strconv.ParseInt(string(digits), 16, 32)
	if err != nil {
		return false
	}
	return v <= 0x10FFFF && !(0xD800 <= v && v <= 0xDFFF)
}
