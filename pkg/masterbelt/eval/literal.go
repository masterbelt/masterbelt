package eval

import (
	"strconv"
	"time"
)

// instantLayouts are the accepted ISO-8601 spellings after a datetime
// literal's D prefix, with and without the millisecond part — the same
// shapes the lexer validated.
var instantLayouts = [...]string{"2006-01-02T15:04:05.000Z07:00", "2006-01-02T15:04:05Z07:00"}

// DatetimeMillis normalizes a datetime literal's text (the D prefix included)
// to a UTC instant in epoch milliseconds. It reports false for a malformed
// literal — one the lexer already diagnosed — so a broken literal never folds
// to a value. The editor's literal hover shares it to show the canonical
// instant.
func DatetimeMillis(text string) (int64, bool) {
	if len(text) < 2 || text[0] != 'D' {
		return 0, false
	}
	for _, layout := range instantLayouts {
		if t, err := time.Parse(layout, text[1:]); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
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
