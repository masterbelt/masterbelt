package lsp

import (
	"encoding/json"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// The editor's view of a function literal: what the checker settled for its
// signature (semantic.Program.FuncLitTypes) rendered back at the literal's
// tokens — an inlay hint where an annotation was omitted, a hover on a
// parameter name. The helpers here bridge the solved types (keyed by AST
// node) to source positions (the literal's concrete tree).

// lambdaHints renders the inferred parts of each function literal's signature
// as inlay hints: ": T" after an unannotated parameter's name, and after the
// parameter list when the result type was inferred or pushed in. Written
// annotations already show themselves; an unsolved (invalid) part has nothing
// to show. Like the constant hints, each hint carries a TextEdit that
// materializes the annotation.
func lambdaHints(doc view, trees map[cst.Green]cst.Tree, startOff, endOff int) []protocol.InlayHint {
	buf := doc.Buffer()
	kind := protocol.InlayHintKindType

	var hints []protocol.InlayHint
	add := func(at int, text string) {
		if at < startOff || at > endOff {
			return
		}
		label, err := json.Marshal(text)
		if err != nil {
			return
		}
		hints = append(hints, protocol.InlayHint{
			Position:  toPosition(buf, at),
			Label:     label,
			Kind:      &kind,
			TextEdits: []protocol.TextEdit{{Range: toRange(buf, at, at), NewText: text}},
		})
	}

	types := doc.FuncLitTypes()
	forEachFuncLit(doc, func(lit *ast.FuncLit) {
		t := types[lit]
		if t == nil {
			return
		}
		tree, ok := trees[lit.Syntax()]
		if !ok {
			return
		}
		params, paramsEnd := litParams(tree)
		for i, p := range lit.Params {
			if p.Type != nil || i >= len(params) || i >= len(t.Params) || t.Params[i] == ir.Invalid {
				continue
			}
			if nameTok, ok := nameToken(params[i]); ok {
				add(nameTok.End(), ": "+t.Params[i].String())
			}
		}
		if lit.Result == nil && t.Result != ir.Invalid && paramsEnd >= 0 {
			add(paramsEnd, ": "+t.Result.String())
		}
	})
	return hints
}

// lambdaParamHover describes the function-literal parameter denoted at offset:
// its name in the parameter list, or a reference to it inside the literal's
// body. The innermost literal containing the offset is consulted first, so a
// nested literal's parameter shadows an outer one's — the same way the body's
// own scope resolves the name.
func lambdaParamHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	buf := doc.Buffer()
	tok, name, ok := identAt(doc.AST().Concrete().Tree(), buf, offset)
	if !ok {
		return nil
	}

	types := doc.FuncLitTypes()
	// The literals containing the offset form a nesting chain; forEachFuncLit
	// visits outer before inner, so the chain arrives outermost first.
	var enclosing []*ast.FuncLit
	forEachFuncLit(doc, func(lit *ast.FuncLit) {
		if t, ok := trees[lit.Syntax()]; ok && within(t, offset) {
			enclosing = append(enclosing, lit)
		}
	})
	for i := len(enclosing) - 1; i >= 0; i-- {
		lit := enclosing[i]
		t := types[lit]
		if t == nil {
			continue
		}
		for j, p := range lit.Params {
			if p.Name != name || j >= len(t.Params) || t.Params[j] == ir.Invalid {
				continue
			}
			r := toRange(buf, tok.Offset(), tok.End())
			return &protocol.Hover{
				Contents: protocol.MarkupContent{
					Kind:  protocol.Markdown,
					Value: "```masterbelt\n" + name + ": " + t.Params[j].String() + "\n```",
				},
				Range: &r,
			}
		}
	}
	return nil
}

// forEachFuncLit visits every function literal in the document, in source
// order, an enclosing literal before the ones nested in its body.
func forEachFuncLit(doc view, fn func(*ast.FuncLit)) {
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if lit, ok := e.(*ast.FuncLit); ok {
			fn(lit)
		}
	})
}

// forEachExpr visits every expression of a file — constant initializers,
// assert conditions, and the bodies of every kind of method and function
// (a type's and an enum's impl methods, an interface's provided defaults, a
// master's per-row methods, and top-level functions), descending into a
// statement body's full control flow and into function-literal bodies — an
// enclosing expression before the ones nested in it.
//
// The statement-level walk delegates to the shared ast.WalkBodyExprs so the
// editor reaches every expression a let initializer, an assignment, a switch
// arm, or an if branch holds; the expression-level recursion delegates to
// ast.WalkExprs so a new operand position (a ternary's branches, say) is wired
// in once, in the AST package, for every walk that layers on it.
func forEachExpr(file *ast.File, fn func(ast.Expr)) {
	var walkExpr func(e ast.Expr)
	walkExpr = func(e ast.Expr) {
		if e == nil {
			return
		}
		// WalkExprs reports e and descends its operands (a member's receiver, a
		// call's callee and arguments, a ternary's branches, a collection's and
		// a record's values) — but, by design, not a function literal's body,
		// which is its own scope. Drive into that body here with the shared
		// statement walk, so a call or member nested in a lambda is reached too.
		ast.WalkExprs(e, func(inner ast.Expr) bool {
			fn(inner)
			if lit, ok := inner.(*ast.FuncLit); ok {
				ast.WalkBodyExprs(lit.Body, walkExpr)
			}
			return true
		})
	}
	walkBody := func(body []ast.Stmt) { ast.WalkBodyExprs(body, walkExpr) }

	for _, decl := range file.Decls {
		if decl.Value != nil {
			walkExpr(decl.Value)
		}
	}
	for _, a := range file.Asserts {
		if a.Cond != nil {
			walkExpr(a.Cond)
		}
	}
	for _, td := range file.Types {
		for _, m := range td.Methods {
			walkBody(m.Body)
		}
	}
	for _, ed := range file.Enums {
		for _, m := range ed.Methods {
			walkBody(m.Body)
		}
	}
	for _, id := range file.Interfaces {
		for _, m := range id.Members {
			walkBody(m.Body)
		}
	}
	for _, fd := range file.Funcs {
		walkBody(fd.Body)
	}
	for _, md := range file.Masters {
		for _, m := range md.Methods {
			walkBody(m.Body)
		}
	}
}

// litParams returns the positioned Param nodes of a function literal's
// parameter list and the offset just after its ")" (-1 when the list is
// malformed), where a result-type hint would sit.
func litParams(lit cst.Tree) ([]cst.Tree, int) {
	for _, child := range lit.Children() {
		if k, ok := child.Kind(); ok && k == cst.ParamList {
			var params []cst.Tree
			for _, pc := range child.Children() {
				if pk, ok := pc.Kind(); ok && pk == cst.Param {
					params = append(params, pc)
				}
			}
			return params, child.End()
		}
	}
	return nil, -1
}

// identAt returns the positioned identifier token at offset whose parent is a
// name reference, a parameter, or a let binding — the positions where an
// identifier denotes a value — together with its text. A let's bound name is the
// bare identifier directly under the LetStmt (its type and value nest in their
// own clauses), so matching the direct Ident child reaches the declaration
// without the reference forms.
func identAt(root cst.Tree, buf source.Buffer, offset int) (cst.Tree, string, bool) {
	var found cst.Tree
	var ok bool
	var walk func(t cst.Tree)
	walk = func(t cst.Tree) {
		if ok || !within(t, offset) {
			return
		}
		if k, isNode := t.Kind(); isNode && (k == cst.NameRef || k == cst.Param || k == cst.LetStmt || k == cst.MatchPattern || k == cst.ForStmt) {
			for _, c := range t.Children() {
				if tok, isTok := c.Token(); isTok && tok.Kind() == token.Ident && within(c, offset) {
					found, ok = c, true
					return
				}
			}
		}
		for _, c := range t.Children() {
			walk(c)
		}
	}
	walk(root)
	if !ok {
		return cst.Tree{}, "", false
	}
	return found, found.Text(buf), true
}
