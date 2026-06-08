package cli

import (
	"fmt"
	"strings"
)

// unifiedDiff renders the change turning a into b as a unified diff with the
// conventional three lines of context, headed by git-style a/ and b/ labels.
//
// It is a plain line-based LCS diff: `masterbelt fmt --diff` shows what
// formatting would change, the inputs are one source file each, so the O(n·m)
// table is never a concern, and keeping it in-process means the command needs
// no external diff tool and is deterministic across platforms.
func unifiedDiff(fromName, toName, a, b string) string {
	if a == b {
		return ""
	}
	aLines := splitKeepEnds(a)
	bLines := splitKeepEnds(b)
	ops := diffScript(aLines, bLines)

	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n", fromName)
	fmt.Fprintf(&out, "+++ b/%s\n", toName)
	for _, h := range hunks(ops, 3) {
		writeHunk(&out, ops, h.start, h.end)
	}
	return out.String()
}

// dop is one line-level edit: kept (' '), removed ('-'), or added ('+'), paired
// with the line text (its trailing newline included, when it has one).
type dop struct {
	tag  byte
	text string
}

// diffScript turns the two line slices into the edit script that rewrites a
// into b, choosing kept lines along a longest common subsequence so the diff is
// minimal.
func diffScript(a, b []string) []dop {
	c := lcsTable(a, b)
	var ops []dop
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, dop{' ', a[i]})
			i++
			j++
		case c[i+1][j] >= c[i][j+1]:
			ops = append(ops, dop{'-', a[i]})
			i++
		default:
			ops = append(ops, dop{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, dop{'-', a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, dop{'+', b[j]})
	}
	return ops
}

// lcsTable builds the longest-common-subsequence length table, where c[i][j] is
// the LCS length of a[i:] and b[j:].
func lcsTable(a, b []string) [][]int {
	n, m := len(a), len(b)
	c := make([][]int, n+1)
	for i := range c {
		c[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				c[i][j] = c[i+1][j+1] + 1
			} else if c[i+1][j] >= c[i][j+1] {
				c[i][j] = c[i+1][j]
			} else {
				c[i][j] = c[i][j+1]
			}
		}
	}
	return c
}

// hunk is a half-open op range [start,end) shown as one @@ block.
type hunk struct{ start, end int }

// hunks groups the change ops into hunks, padding each with up to context kept
// lines and merging two changes separated by no more than 2·context kept lines
// into one block — the standard unified-diff windowing.
func hunks(ops []dop, context int) []hunk {
	var hs []hunk
	n := len(ops)
	for i := 0; i < n; {
		if ops[i].tag == ' ' {
			i++
			continue
		}
		// A change starts at i. Walk forward, merging across short runs of
		// kept lines, until a run longer than 2·context (or the end) closes it.
		last := i
		j := i
		for j < n {
			if ops[j].tag != ' ' {
				last = j
				j++
				continue
			}
			k := j
			for k < n && ops[k].tag == ' ' {
				k++
			}
			if k >= n || k-j > 2*context {
				break
			}
			j = k
		}
		start := max(0, i-context)
		end := min(n, last+1+context)
		hs = append(hs, hunk{start, end})
		i = end
	}
	return hs
}

// writeHunk emits one @@ block: the header with the 1-based start lines and
// counts on each side, then every op in the range tagged and printed.
func writeHunk(out *strings.Builder, ops []dop, start, end int) {
	aBefore := countSide(ops[:start], '-')
	bBefore := countSide(ops[:start], '+')
	aCount := countSide(ops[start:end], '-')
	bCount := countSide(ops[start:end], '+')

	fmt.Fprintf(out, "@@ -%s +%s @@\n", span(aBefore, aCount), span(bBefore, bCount))
	for _, o := range ops[start:end] {
		out.WriteByte(o.tag)
		out.WriteString(o.text)
		if !strings.HasSuffix(o.text, "\n") {
			out.WriteString("\n\\ No newline at end of file\n")
		}
	}
}

// span renders a unified-diff line range: "start,count", or just the line
// number when count is 1, and "before,0" for an empty side (an insertion or
// deletion point), all 1-based.
func span(before, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", before)
	}
	if count == 1 {
		return fmt.Sprintf("%d", before+1)
	}
	return fmt.Sprintf("%d,%d", before+1, count)
}

// countSide counts the lines an edit script contributes to one side: removed
// and kept lines for '-', added and kept lines for '+'.
func countSide(ops []dop, side byte) int {
	n := 0
	for _, o := range ops {
		if o.tag == ' ' || o.tag == side {
			n++
		}
	}
	return n
}

// splitKeepEnds splits s into lines, keeping each line's trailing newline (the
// final line has none when s does not end in a newline). An empty string is no
// lines.
func splitKeepEnds(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if last := len(lines) - 1; lines[last] == "" {
		lines = lines[:last]
	}
	return lines
}
