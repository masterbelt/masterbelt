package lint

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// unreachableCode reports, for every function and method body, the run of
// statements after one that always diverges. The analysis is purely structural
// and entirely function-local — masterbelt has no panic, so divergence is
// return coverage — which is why it needs only the body, not the whole program.
func (l *linter) unreachableCode(m *ir.Module) {
	for _, fn := range m.Funcs {
		if fn != nil && !fn.Extern {
			l.bodyOf(fn.Syntax, fn.Body)
		}
	}
	for _, def := range m.Types {
		for _, meth := range def.Methods {
			if meth != nil && !meth.Extern {
				l.bodyOf(meth.Syntax, meth.Body)
			}
		}
	}
}

// bodyOf walks a declaration's body unless the declaration already carries an
// error, in which case its body is reported wrong rather than dead.
func (l *linter) bodyOf(decl ast.Node, body []ir.Stmt) {
	if len(body) == 0 {
		return
	}
	if off, width := l.span(decl); width > 0 && l.brokenWithin(off, width) {
		return
	}
	l.checkBlock(body)
}

// checkBlock reports the unreachable tail of a block — the run after the first
// statement that always diverges, reported as one faded span — and recurses
// into the nested blocks of its reachable statements. It does not descend into
// the dead tail: code dead inside dead code is reported once, at the top.
func (l *linter) checkBlock(body []ir.Stmt) {
	for i, s := range body {
		l.recurse(s)
		if diverges(s) {
			l.reportTail(body[i+1:])
			return
		}
	}
}

// recurse descends into a statement's nested blocks, each checked in full. A
// statement with no block — return, expr, let, assign — does nothing here.
func (l *linter) recurse(s ir.Stmt) {
	switch s := s.(type) {
	case *ir.If:
		l.checkBlock(s.Then)
		if s.ElseIf != nil {
			l.recurse(s.ElseIf)
		}
		if s.Else != nil {
			l.checkBlock(s.Else)
		}
	case *ir.For:
		l.checkBlock(s.Body)
	case *ir.Switch:
		for _, arm := range s.Arms {
			l.checkBlock(arm.Body)
		}
		if s.Else != nil {
			l.checkBlock(s.Else)
		}
	case *ir.Match:
		for _, arm := range s.Arms {
			l.checkBlock(arm.Body)
		}
		if s.Else != nil {
			l.checkBlock(s.Else)
		}
	}
}

// reportTail reports a non-empty run of unreachable statements as one faded
// span, from the first dead statement to the end of the last. A statement with
// no anchor leaves the span unsaid rather than pointing nowhere.
func (l *linter) reportTail(tail []ir.Stmt) {
	if len(tail) == 0 {
		return
	}
	start, width := l.span(ir.SyntaxOfStmt(tail[0]))
	if width == 0 {
		return
	}
	if last := ir.SyntaxOfStmt(tail[len(tail)-1]); last != nil {
		if lo, lw := l.span(last); lo+lw > start+width {
			width = lo + lw - start
		}
	}
	l.diags = append(l.diags, unreachable(start, width))
}

// diverges reports whether a statement always transfers control away, so the
// statement after it can never run. masterbelt has no panic, so divergence is
// return coverage: a return diverges; an if/switch/match diverges only when
// every one of its paths does — and a path that can fall through (an if with no
// else, a switch/match with no wildcard) means it does not.
func diverges(s ir.Stmt) bool {
	switch s := s.(type) {
	case *ir.Return:
		return true
	case *ir.If:
		if !blockDiverges(s.Then) {
			return false
		}
		switch {
		case s.ElseIf != nil:
			return diverges(s.ElseIf)
		case s.Else != nil:
			return blockDiverges(s.Else)
		default:
			return false
		}
	case *ir.Switch:
		return s.Else != nil && armsDiverge(switchBodies(s.Arms), s.Else)
	case *ir.Match:
		return s.Else != nil && armsDiverge(matchBodies(s.Arms), s.Else)
	default:
		return false
	}
}

// armsDiverge reports whether every arm body and the wildcard body diverge —
// the condition for an exhausted switch or match to leave no path open.
func armsDiverge(arms [][]ir.Stmt, wildcard []ir.Stmt) bool {
	for _, body := range arms {
		if !blockDiverges(body) {
			return false
		}
	}
	return blockDiverges(wildcard)
}

func switchBodies(arms []ir.SwitchArm) [][]ir.Stmt {
	out := make([][]ir.Stmt, len(arms))
	for i, a := range arms {
		out[i] = a.Body
	}
	return out
}

func matchBodies(arms []ir.MatchArm) [][]ir.Stmt {
	out := make([][]ir.Stmt, len(arms))
	for i, a := range arms {
		out[i] = a.Body
	}
	return out
}

// blockDiverges reports whether a block always diverges: it holds a diverging
// statement, after which the rest is unreachable.
func blockDiverges(body []ir.Stmt) bool {
	for _, s := range body {
		if diverges(s) {
			return true
		}
	}
	return false
}

// unreachable builds the unreachable-code diagnostic, tagged Unnecessary so an
// editor fades the span rather than underlining it.
func unreachable(offset, width int) diagnostic.Diagnostic {
	d := newUnreachableCodeDiagnostic(offset, width)
	d.Tags = []diagnostic.Tag{diagnostic.TagUnnecessary}
	return d
}
