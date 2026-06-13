package lsp

import (
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Hover cards for the members of a type: a method access or declaration, a
// method parameter, and the record field a receiver reads. Resolving the
// receiver's type (receiverTypeOf, paramTypeAt, fieldOf) and rendering a
// method's signature (methodSignature, methodSignatureSubst) live here too.

// methodParamHover describes the method parameter denoted at offset: its name
// in the signature's parameter list, or a reference to it inside the method's
// body. The type comes from the module's resolved signature — each resolved
// method carries its declaration (ir.Method.Syntax), so the pairing holds
// across overloads — and renders as the checker sees it. Function literals
// nest inside method bodies and their parameters shadow the method's —
// lambdaParamHover runs first.
func methodParamHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	buf := doc.Buffer()
	tok, name, ok := identAt(doc.AST().Concrete().Tree(), buf, offset)
	if !ok {
		return nil
	}

	for _, def := range doc.Module().Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			mt, found := trees[irm.Syntax.Syntax()]
			if !found || !within(mt, offset) {
				continue
			}
			for _, p := range irm.Params {
				if p.Name != name || p.Type == nil || p.Type == ir.Invalid {
					continue
				}
				r := toRange(buf, tok.Offset(), tok.End())
				return &protocol.Hover{
					Contents: protocol.MarkupContent{
						Kind:  protocol.Markdown,
						Value: "```masterbelt\n" + name + ": " + p.Type.String() + "\n```",
					},
					Range: &r,
				}
			}
		}
	}
	return nil
}

// methodSignature renders one method as it is declared: modifiers, name,
// parameters, and result, in source syntax.
func methodSignature(m *ir.Method) string {
	return methodSignatureSubst(m, nil)
}

// methodSignatureSubst is methodSignature with the receiver's solved type
// arguments substituted in, so list<int8>.map shows fn(item: int8).
func methodSignatureSubst(m *ir.Method, subst map[string]ir.Type) string {
	render := func(t ir.Type) string {
		if t == nil {
			return ""
		}
		if len(subst) > 0 {
			t = types.Substitute(t, subst)
		}
		return t.String()
	}
	var b strings.Builder
	if m.Public {
		b.WriteString("pub ")
	}
	if m.Extern {
		b.WriteString("extern ")
	}
	// The accessor/static modifier leads the signature, the way it is written:
	// get name / set name / static fn name. An ordinary method carries none.
	switch m.Kind {
	case ir.MethodGetter:
		b.WriteString("get ")
	case ir.MethodSetter:
		b.WriteString("set ")
	case ir.MethodStatic:
		b.WriteString("static fn ")
	default:
		// An ordinary method (MethodNormal) carries no leading modifier:
		// nothing is written before its effects and name.
	}
	for _, eff := range m.Effects {
		b.WriteString(eff + " ")
	}
	b.WriteString(m.Name)
	b.WriteString("(")
	for i, p := range m.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.Name)
		if p.Type != nil {
			b.WriteString(": " + render(p.Type))
		}
	}
	b.WriteString(")")
	if m.Result != nil {
		b.WriteString(": " + render(m.Result))
	}
	return b.String()
}

// memberHover describes the member access at offset: the method the
// receiver's type binds for the name — its signature with the receiver's
// generic arguments substituted in, and its doc — or the record field it
// reads, with its type.
func memberHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	leaf, parent, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil
	}
	if k, isTok := leaf.TokenKind(); !isTok || k != token.Ident {
		return nil
	}
	if pk, isNode := parent.Kind(); !isNode || pk != cst.MemberExpr {
		return nil
	}
	parentNode, _ := parent.Node()

	// The AST member access backing this node. Operators desugar to synthetic
	// member accesses, but those share their operator's CST node, never a
	// MemberExpr node — only an access written in the source matches here.
	var member *ast.MemberExpr
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if m, ok := e.(*ast.MemberExpr); ok && m.Syntax() == parentNode {
			member = m
		}
	})
	if member == nil {
		return nil
	}

	recv := receiverTypeOf(doc, member.Receiver, trees, offset)
	if recv == nil || recv == ir.Invalid {
		return nil
	}
	name := member.Member.Name
	r := toRange(doc.Buffer(), leaf.Offset(), leaf.End())

	if ms, subst, ok := doc.MethodCandidates(recv, name); ok {
		return memberMethodHover(ms, subst, r)
	}
	if f, ok := memberFieldOf(recv, name); ok {
		return memberFieldHover(f, r)
	}
	if g, subst, ok := receiverGetter(doc, recv, name); ok {
		return memberGetterHover(g, subst, r)
	}
	return nil
}

// memberMethodHover builds the method card for a member access: a single
// signature with its doc below, or — for an overloaded name — every signature
// listed each under its own doc comment, reading like the impl block itself.
func memberMethodHover(ms []*ir.Method, subst map[string]ir.Type, r protocol.Range) *protocol.Hover {
	var b strings.Builder
	if len(ms) == 1 {
		// The common single-signature card: the signature, its doc below.
		b.WriteString("```masterbelt\n")
		b.WriteString(methodSignatureSubst(ms[0], subst))
		b.WriteString("\n```")
		if len(ms[0].Doc) > 0 {
			b.WriteString("\n\n")
			b.WriteString(strings.Join(ms[0].Doc, "\n"))
		}
	} else {
		// An overloaded name lists every signature, each under its own doc
		// comment — the card reads like the impl block itself.
		b.WriteString("```masterbelt\n")
		for i, m := range ms {
			if i > 0 {
				b.WriteString("\n")
			}
			for _, doc := range m.Doc {
				b.WriteString("/// " + doc + "\n")
			}
			b.WriteString(methodSignatureSubst(m, subst))
			b.WriteString("\n")
		}
		b.WriteString("```")
	}
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}

// memberFieldHover builds the field card for a member access: the field it
// reads, with its type.
func memberFieldHover(f ir.Field, r protocol.Range) *protocol.Hover {
	return &protocol.Hover{
		Contents: protocol.MarkupContent{
			Kind:  protocol.Markdown,
			Value: "```masterbelt\n" + f.Name + ": " + f.Type.String() + "\n```",
		},
		Range: &r,
	}
}

// memberGetterHover builds the card for a getter read (value.name): its
// declaration card, the same shape a method's is — the signature with the
// receiver's generic arguments substituted, its doc below.
func memberGetterHover(g *ir.Method, subst map[string]ir.Type, r protocol.Range) *protocol.Hover {
	var b strings.Builder
	b.WriteString("```masterbelt\n")
	b.WriteString(methodSignatureSubst(g, subst))
	b.WriteString("\n```")
	if len(g.Doc) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(g.Doc, "\n"))
	}
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}

// receiverGetter finds the getter named name on the receiver's type, with the
// receiver's substitution, for a getter-read hover. It reads the kind-tagged
// methods from ReceiverMethods (which returns every kind) and keeps the getter.
func receiverGetter(doc view, recv ir.Type, name string) (*ir.Method, map[string]ir.Type, bool) {
	methods, subst, ok := doc.ReceiverMethods(recv)
	if !ok {
		return nil, nil, false
	}
	for _, m := range methods {
		if m.Kind == ir.MethodGetter && m.Name == name {
			return m, subst, true
		}
	}
	return nil, nil, false
}

// receiverTypeOf resolves the type a member access's receiver has: self is
// the enclosing impl's type, an identifier is the constant it names or the
// parameter it denotes, a namespace member is the constant it imports, and a
// chained access is the field's type on the inner receiver. Anything else —
// a collection literal, an operator chain — goes through the real inference
// in the file's top-level scope.
func receiverTypeOf(doc view, e ast.Expr, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	switch e := e.(type) {
	case *ast.SelfExpr:
		if def := enclosingMethodOwner(doc, trees, offset); def != nil {
			return &ir.Named{Def: def}
		}
		return nil
	case *ast.Identifier:
		if c := doc.Resolve(e); c != nil {
			return c.Type
		}
		return paramTypeAt(doc, e.Name, trees, offset)
	case *ast.MemberExpr:
		if c := doc.ResolveMember(e); c != nil {
			return c.Type
		}
		if inner := receiverTypeOf(doc, e.Receiver, trees, offset); inner != nil {
			if f, ok := memberFieldOf(inner, e.Member.Name); ok {
				return f.Type
			}
		}
		return nil
	}
	if t := doc.TypeOfExpr(e); t != ir.Invalid {
		return t
	}
	return nil
}

// enclosingMethodOwner returns the type definition whose impl method body spans
// offset — what self denotes there — across every kind that carries methods: a
// type, an enum, or a master's per-row impl. It reads the resolved methods off
// the IR definitions (each method links back to its declaration syntax), so a
// master method, indexed nowhere in file.Types, resolves the same as a type's.
func enclosingMethodOwner(doc view, trees map[cst.Green]cst.Tree, offset int) *ir.TypeDef {
	for _, def := range doc.Module().Types {
		for _, m := range def.Methods {
			if m.Syntax == nil {
				continue
			}
			if t, ok := trees[m.Syntax.Syntax()]; ok && within(t, offset) {
				return def
			}
		}
		// A master's per-row validate checks read the row through self, so an
		// offset inside a validate clause resolves self to the master the same way
		// a row method body does — completion and hover then see the row's fields.
		if def.MasterSyntax != nil {
			for _, c := range def.MasterSyntax.Validations {
				if t, ok := trees[c.Syntax()]; ok && within(t, offset) {
					return def
				}
			}
		}
	}
	return nil
}

// paramTypeAt resolves name as a parameter of what encloses offset: the
// innermost function literal first (its parameters shadow the method's),
// then the method's signature. A self-typed parameter resolves to the
// enclosing impl's type, so its methods bind through it.
func paramTypeAt(doc view, name string, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	if t := funcLitParamTypeAt(doc, name, trees, offset); t != nil {
		return t
	}
	if t := methodParamTypeAt(doc, name, trees, offset); t != nil {
		return t
	}
	return funcParamTypeAt(doc, name, trees, offset)
}

// funcLitParamTypeAt resolves name as a parameter of the innermost function
// literal enclosing offset — its parameters shadow the method's, so the
// enclosing literals are scanned innermost-first.
func funcLitParamTypeAt(doc view, name string, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	litTypes := doc.FuncLitTypes()
	var enclosing []*ast.FuncLit
	forEachFuncLit(doc, func(lit *ast.FuncLit) {
		if t, ok := trees[lit.Syntax()]; ok && within(t, offset) {
			enclosing = append(enclosing, lit)
		}
	})
	for i := len(enclosing) - 1; i >= 0; i-- {
		lit := enclosing[i]
		ft := litTypes[lit]
		if ft == nil {
			continue
		}
		for j, p := range lit.Params {
			if p.Name == name && j < len(ft.Params) && ft.Params[j] != ir.Invalid {
				return ft.Params[j]
			}
		}
	}
	return nil
}

// methodParamTypeAt resolves name as a parameter of the method whose body
// spans offset. A self-typed parameter resolves to the enclosing impl's type,
// so its methods bind through it.
func methodParamTypeAt(doc view, name string, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	for _, def := range doc.Module().Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			mt, ok := trees[irm.Syntax.Syntax()]
			if !ok || !within(mt, offset) {
				continue
			}
			for _, p := range irm.Params {
				if p.Name != name || p.Type == nil || p.Type == ir.Invalid {
					continue
				}
				if _, isSelf := p.Type.(*ir.SelfType); isSelf {
					return &ir.Named{Def: def}
				}
				return p.Type
			}
		}
	}
	return nil
}

// funcParamTypeAt resolves name as a parameter of the top-level function body
// spanning offset. A generic function's parameter is a bare TypeVar in the
// resolved signature, so the type-parameter bounds are rebound onto it
// (BindTypeParamBounds) — a `c: T` where T: foldable then surfaces the bound
// interface's methods on c.
func funcParamTypeAt(doc view, name string, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	for _, f := range doc.Module().Funcs {
		if f.Syntax == nil {
			continue
		}
		ft, ok := trees[f.Syntax.Syntax()]
		if !ok || !within(ft, offset) {
			continue
		}
		bindBounds := infer.BindTypeParamBounds(f.TypeParams)
		for _, p := range f.Params {
			if p.Name != name || p.Type == nil || p.Type == ir.Invalid {
				continue
			}
			return types.Substitute(p.Type, bindBounds)
		}
	}
	return nil
}

// fieldOf returns the record field a type carries under name — directly, or
// through a named type's record body. Like recordOf, it does not look through a
// master: the record-literal paths keep a master opaque. The value member-access
// read paths use memberFieldOf, which projects the master row.
func fieldOf(t ir.Type, name string) (ir.Field, bool) {
	switch t := t.(type) {
	case *ir.Record:
		for _, f := range t.Fields {
			if f.Name == name {
				return f, true
			}
		}
	case *ir.Named:
		if t.Def != nil {
			return fieldOf(t.Def.Body, name)
		}
	}
	return ir.Field{}, false
}

// memberFieldOf returns the record field a value member access reads under name,
// through the same types.RecordOf the checker's member access uses — so it
// follows a named alias, instantiates a generic application, and projects a
// master's row, including a master reached through an alias. A master row field
// is readable through a value (s.field) though the master is not constructible,
// so this read path projects the row while fieldOf keeps the literal paths opaque.
func memberFieldOf(t ir.Type, name string) (ir.Field, bool) {
	if rec := types.RecordOf(t); rec != nil {
		return fieldOf(rec, name)
	}
	return ir.Field{}, false
}

// methodDeclHover describes the method declared at offset — the cursor on its
// name in an impl block — as its resolved signature and doc.
func methodDeclHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	leaf, parent, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil
	}
	if k, isTok := leaf.TokenKind(); !isTok || k != token.Ident {
		return nil
	}
	if pk, isNode := parent.Kind(); !isNode || pk != cst.MethodDecl {
		return nil
	}

	for _, def := range doc.Module().Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			mt, found := trees[irm.Syntax.Syntax()]
			if !found || !within(mt, offset) {
				continue
			}
			var b strings.Builder
			b.WriteString("```masterbelt\n")
			b.WriteString(methodSignature(irm))
			b.WriteString("\n```")
			if len(irm.Doc) > 0 {
				b.WriteString("\n\n")
				b.WriteString(strings.Join(irm.Doc, "\n"))
			}
			r := toRange(doc.Buffer(), leaf.Offset(), leaf.End())
			return &protocol.Hover{
				Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
				Range:    &r,
			}
		}
	}
	return nil
}

// enumMemberHover describes an enum member access at offset (Rarity.Common):
// the qualified member name and its base value, rendered as
// `Rarity.Common = 1` with the member's doc comments beneath. It returns nil
// when offset is not on an enum member access (the other hover paths handle
// methods, fields, and values).
func enumMemberHover(doc view, offset int) *protocol.Hover {
	member, ok := memberAccessAt(doc, offset)
	if !ok {
		return nil
	}
	recv, ok := member.Receiver.(*ast.Identifier)
	if !ok || doc.Resolve(recv) != nil {
		return nil // a value shadowing the type name is a value access
	}
	def := lookupEnumType(doc, recv.Name)
	if def == nil {
		return nil
	}
	idx := -1
	for i, m := range def.Enum.Members {
		if m.Name == member.Member.Name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	m := def.Enum.Members[idx]
	var b strings.Builder
	b.WriteString("```masterbelt\n")
	b.WriteString(def.Name)
	b.WriteString(".")
	b.WriteString(m.Name)
	if m.Value != nil {
		b.WriteString(" = ")
		b.WriteString(m.Value.String())
	}
	b.WriteString("\n```")

	node, _ := memberNodeAt(doc, offset)
	rng := cst.Root(node)
	r := toRange(doc.Buffer(), rng.Offset(), rng.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}

// assocConstHover describes an associated-constant access at offset (int8.Max,
// Level.Max): the qualified name with its type, its folded value, and the
// constant's doc comments beneath — rendered as `int8.Max: int = 127`. It
// returns nil when offset is not on an associated-constant access (the enum and
// value hover paths claim their own forms first).
func assocConstHover(doc view, offset int) *protocol.Hover {
	member, ok := memberAccessAt(doc, offset)
	if !ok {
		return nil
	}
	recv, ok := member.Receiver.(*ast.Identifier)
	if !ok || doc.Resolve(recv) != nil {
		return nil // a value shadowing the type name is a value access
	}
	def := lookupTypeName(doc, recv.Name)
	if def == nil {
		return nil
	}
	var c *ir.AssocConst
	for _, ac := range def.Consts {
		if ac.Name == member.Member.Name {
			c = ac
			break
		}
	}
	if c == nil {
		return nil
	}
	var b strings.Builder
	b.WriteString("```masterbelt\n")
	b.WriteString(def.Name)
	b.WriteString(".")
	b.WriteString(c.Name)
	if c.Type != nil && c.Type != ir.Invalid {
		b.WriteString(": ")
		b.WriteString(c.Type.String())
	}
	if c.Value != nil {
		b.WriteString(" = ")
		b.WriteString(c.Value.String())
	}
	b.WriteString("\n```")
	if len(c.Doc) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(c.Doc, "\n"))
	}

	node, _ := memberNodeAt(doc, offset)
	rng := cst.Root(node)
	r := toRange(doc.Buffer(), rng.Offset(), rng.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}

// staticCallHover describes a static fn call's callee at offset (Celsius.freezing):
// each static fn overload of that name on the type, its signature and doc — the
// Type.name(...) twin of the method hover card. It returns nil when offset is not
// on a static-fn member access (the enum, associated-constant, and value hover
// paths claim their forms first).
func staticCallHover(doc view, offset int) *protocol.Hover {
	member, ok := memberAccessAt(doc, offset)
	if !ok {
		return nil
	}
	recv, ok := member.Receiver.(*ast.Identifier)
	if !ok || doc.Resolve(recv) != nil {
		return nil // a value shadowing the type name is a value access
	}
	def := lookupTypeName(doc, recv.Name)
	if def == nil {
		return nil
	}
	var statics []*ir.Method
	for _, m := range def.Methods {
		if m.Kind == ir.MethodStatic && m.Name == member.Member.Name {
			statics = append(statics, m)
		}
	}
	if len(statics) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("```masterbelt\n")
	for i, m := range statics {
		if i > 0 {
			b.WriteString("\n")
		}
		for _, d := range m.Doc {
			b.WriteString("/// " + d + "\n")
		}
		b.WriteString(methodSignatureSubst(m, nil))
		b.WriteString("\n")
	}
	b.WriteString("```")

	node, _ := memberNodeAt(doc, offset)
	rng := cst.Root(node)
	r := toRange(doc.Buffer(), rng.Offset(), rng.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}
