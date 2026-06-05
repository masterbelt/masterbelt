package eval

import (
	"strconv"
	"strings"
	"time"
)

// instantLayouts are the accepted ISO-8601 spellings after a datetime
// literal's D prefix, with and without the millisecond part — the same
// shapes the lexer validated.
var instantLayouts = [...]string{"2006-01-02T15:04:05.000Z07:00", "2006-01-02T15:04:05Z07:00"}

// DatetimeMillis normalizes a datetime literal's text (the D prefix included)
// to a UTC instant in epoch milliseconds. It reports false for a malformed
// literal — one the lexer already diagnosed — so a broken literal never folds
// to a value: the two layers must accept exactly the same set, which the
// lexer-parity test pins (TestDatetimeLexEvalParity). The editor's literal
// hover shares it to show the canonical instant.
func DatetimeMillis(text string) (int64, bool) {
	if len(text) < 2 || text[0] != 'D' {
		return 0, false
	}
	s := text[1:]
	if !msDigitsOK(s) {
		return 0, false
	}
	for _, layout := range instantLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli(), offsetInRange(s)
		}
	}
	return 0, false
}

// msDigitsOK requires the clock's fractional part to be exactly three digits
// (or absent) — the lexer's rule. time.Parse alone would accept any width
// through its implicit fractional-second rule.
func msDigitsOK(s string) bool {
	i := strings.IndexByte(s, '.')
	if i < 0 {
		return true
	}
	n := 0
	for j := i + 1; j < len(s) && '0' <= s[j] && s[j] <= '9'; j++ {
		n++
	}
	return n == 3 && strings.IndexByte(s[i+1:], '.') < 0
}

// offsetInRange bounds a signed zone offset to ±23:59 — time.Parse is lenient
// about the hour count, but the lexer is not, and the fold must agree with
// the diagnostic. The literal ends with either Z or ±hh:mm, the digits
// guaranteed by the parse.
func offsetInRange(s string) bool {
	if s[len(s)-1] == 'Z' {
		return true
	}
	hh, _ := strconv.Atoi(s[len(s)-5 : len(s)-3])
	mm, _ := strconv.Atoi(s[len(s)-2:])
	return hh <= 23 && mm <= 59
}

// unitMillis is each duration unit's worth in milliseconds.
var unitMillis = map[string]int64{
	"w":  7 * 24 * 60 * 60 * 1000,
	"d":  24 * 60 * 60 * 1000,
	"h":  60 * 60 * 1000,
	"m":  60 * 1000,
	"s":  1000,
	"ms": 1,
}

// DurationMillis totals a duration literal's digit+unit groups into
// milliseconds. It reports false for a malformed literal or a total that
// overflows int64, so neither folds to a value. The editor's literal hover
// shares it to show the canonical decomposition.
func DurationMillis(text string) (int64, bool) {
	var total int64
	i := 0
	for i < len(text) {
		start := i
		for i < len(text) && '0' <= text[i] && text[i] <= '9' {
			i++
		}
		digits := text[start:i]
		start = i
		for i < len(text) && (('a' <= text[i] && text[i] <= 'z') || ('A' <= text[i] && text[i] <= 'Z')) {
			i++
		}
		worth, ok := unitMillis[text[start:i]]
		if digits == "" || !ok {
			return 0, false
		}
		n, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return 0, false // more digits than int64 holds
		}
		group := n * worth
		if n != 0 && group/n != worth {
			return 0, false // the group alone overflows
		}
		total += group
		if total < 0 {
			return 0, false // the running total overflowed
		}
	}
	return total, total >= 0
}
