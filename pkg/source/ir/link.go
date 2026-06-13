// This file is the second pass of the IR's text round trip: UnmarshalText
// rebuilds the trees with placeholder targets (a *Const carrying only the
// referenced name, a *Method carrying "Owner.name(sig)"), and Link walks the
// module once, swapping every placeholder for the declaration it names — the
// same two-pass shape the semantic assembler uses (shells first, bodies
// after). References the module does not declare resolve through the
// caller's Resolver; a name nobody supplies is a loud error, never a silent
// dangling pointer.

package ir

import (
	"fmt"
	"strings"
)

// Resolver supplies the declarations a module's references name when the
// module itself does not declare them: the prelude's types, a used file's
// constants and functions. A nil func (or a nil return) means "not supplied",
// which Link reports as an unresolved reference. Functions resolve by the
// reference form name(sig); types and constants by bare name.
type Resolver struct {
	Const    func(name string) *Const
	TypeDef  func(name string) *TypeDef
	Function func(ref string) *Function
}

// Link resolves every reference placeholder an unmarshaled module carries,
// in place. It must run exactly once, on a freshly unmarshaled module; the
// result is a fully connected graph whose unresolved names have all been
// reported. The module's own declarations win; external names fall back to
// the resolver.
func (m *Module) Link(resolve Resolver) error {
	l := &linker{
		consts:  map[string]*Const{},
		types:   map[string]*TypeDef{},
		funcs:   map[string][]*Function{},
		resolve: resolve,
	}
	for _, c := range m.Consts {
		if c != nil {
			l.consts[c.Name] = c
		}
	}
	for _, def := range m.Types {
		if def != nil {
			l.types[def.Name] = def
		}
	}
	for _, fn := range m.Funcs {
		if fn != nil {
			l.funcs[fn.Name] = append(l.funcs[fn.Name], fn)
		}
	}

	for _, c := range m.Consts {
		if c == nil {
			continue
		}
		l.linkType(&c.Type)
		l.linkValue(&c.Value)
		l.linkConstant(c.Eval)
	}
	for _, def := range m.Types {
		l.linkTypeDef(def)
	}
	for _, fn := range m.Funcs {
		l.linkFunction(fn)
	}
	for _, a := range m.Asserts {
		if a != nil {
			l.linkConstant(a.Eval)
		}
	}
	return l.err
}

// linker carries the relink tables and the first error met.
type linker struct {
	consts  map[string]*Const
	types   map[string]*TypeDef
	funcs   map[string][]*Function
	resolve Resolver
	err     error
}

// fail records the first resolution error; later finds keep it.
func (l *linker) fail(format string, args ...any) {
	if l.err == nil {
		l.err = fmt.Errorf(format, args...)
	}
}

// --- the reference resolutions ------------------------------------------------

// resolveConst swaps a constant placeholder for the declaration it names.
func (l *linker) resolveConst(p **Const) {
	if *p == nil {
		return
	}
	name := (*p).Name
	if c, ok := l.consts[name]; ok {
		*p = c
		return
	}
	if l.resolve.Const != nil {
		if c := l.resolve.Const(name); c != nil {
			*p = c
			return
		}
	}
	l.fail("ir: link: unresolved constant reference %q", name)
}

// resolveTypeDef swaps a type-definition placeholder for the declaration.
func (l *linker) resolveTypeDef(p **TypeDef) {
	if *p == nil {
		return
	}
	name := (*p).Name
	if def, ok := l.types[name]; ok {
		*p = def
		return
	}
	if l.resolve.TypeDef != nil {
		if def := l.resolve.TypeDef(name); def != nil {
			*p = def
			return
		}
	}
	l.fail("ir: link: unresolved type reference %q", name)
}

// resolveFunction swaps a function placeholder — its Name holds the
// "name(sig)" reference — for the declared overload whose signature matches.
func (l *linker) resolveFunction(p **Function) {
	if *p == nil {
		return
	}
	ref := (*p).Name
	name, _, ok := strings.Cut(ref, "(")
	if !ok {
		l.fail("ir: link: malformed function reference %q", ref)
		return
	}
	for _, fn := range l.funcs[name] {
		if fn.Name+paramSig(fn.Params) == ref {
			*p = fn
			return
		}
	}
	if l.resolve.Function != nil {
		if fn := l.resolve.Function(ref); fn != nil {
			*p = fn
			return
		}
	}
	l.fail("ir: link: unresolved function reference %q", ref)
}

// resolveMethod swaps a method placeholder — its Name holds the
// "Owner.name(sig)[#kind]" reference — for the owner's matching method.
func (l *linker) resolveMethod(p **Method) {
	if *p == nil {
		return
	}
	ref := (*p).Name
	owner, rest, ok := strings.Cut(ref, ".")
	if !ok || owner == "?" {
		l.fail("ir: link: method reference %q names no owner (marshaled without a module root)", ref)
		return
	}
	def, ok := l.types[owner]
	if !ok && l.resolve.TypeDef != nil {
		def = l.resolve.TypeDef(owner)
		ok = def != nil
	}
	if !ok {
		l.fail("ir: link: method reference %q: unresolved owner %q", ref, owner)
		return
	}
	for _, method := range def.Methods {
		got := method.Name + paramSig(method.Params)
		if method.Kind != MethodNormal {
			got += "#" + method.Kind.String()
		}
		if got == rest {
			*p = method
			return
		}
	}
	l.fail("ir: link: %q has no method matching %q", owner, rest)
}

// --- the owned-edge walks -------------------------------------------------------
//
// The walks descend only through owned edges (a module's lists, a value's
// operands, a type's structural members) and never through the reference
// edges they resolve — a reference is swapped, not entered — so a decoded
// module, which is structurally a tree, needs no visited set.

// linkTypeDef relinks one type definition's contents.
func (l *linker) linkTypeDef(def *TypeDef) {
	if def == nil {
		return
	}
	for _, p := range def.Params {
		if p != nil {
			l.linkType(&p.Bound)
		}
	}
	l.linkType(&def.Body)
	for _, m := range def.Methods {
		l.linkMethod(m)
	}
	for _, c := range def.Consts {
		if c != nil {
			l.linkType(&c.Type)
			l.linkConstant(c.Value)
		}
	}
	if def.Interface != nil {
		for i := range def.Interface.Parents {
			l.linkType(&def.Interface.Parents[i])
		}
	}
	for i := range def.Impls {
		l.linkType(&def.Impls[i])
	}
	if def.Enum != nil {
		for i := range def.Enum.Members {
			l.linkConstant(def.Enum.Members[i].Value)
		}
	}
	if def.Master != nil {
		// The row type carries the same Named/App references a body does, so relink
		// it too — otherwise a master row typed by another declaration stays a
		// detached placeholder after a text round-trip.
		l.linkType(&def.Master.Row)
		// The per-row validate checks are value graphs over the row, carrying the
		// same references a where predicate does, so relink each condition or it
		// stays detached after a round-trip.
		for _, c := range def.Master.RowChecks {
			if c != nil {
				l.linkValue(&c.Cond)
			}
		}
	}
	l.linkValue(&def.Where)
}

// linkMethod relinks a method's signature, owner edge, and body.
func (l *linker) linkMethod(m *Method) {
	if m == nil {
		return
	}
	l.resolveTypeDef(&m.Owner)
	for i := range m.Params {
		l.linkType(&m.Params[i].Type)
	}
	l.linkType(&m.Result)
	l.linkBody(m.Body)
}

// linkFunction relinks a function's signature and body.
func (l *linker) linkFunction(fn *Function) {
	if fn == nil {
		return
	}
	for _, p := range fn.TypeParams {
		if p != nil {
			l.linkType(&p.Bound)
		}
	}
	for i := range fn.Params {
		l.linkType(&fn.Params[i].Type)
	}
	l.linkType(&fn.Result)
	l.linkBody(fn.Body)
}

// linkBody relinks every statement of a body. The switch is exhaustive over
// the sealed Stmt forms; a new form panics rather than silently keeping its
// placeholders.
func (l *linker) linkBody(body []Stmt) {
	for _, s := range body {
		switch s := s.(type) {
		case *Return:
			l.linkValue(&s.Value)
		case *ExprStmt:
			l.linkValue(&s.Value)
		case *Let:
			l.linkType(&s.Type)
			l.linkValue(&s.Value)
		case *Assign:
			l.linkValue(&s.Value)
		case *Switch:
			l.linkValue(&s.Scrutinee)
			for i := range s.Arms {
				for j := range s.Arms[i].Values {
					l.linkValue(&s.Arms[i].Values[j])
				}
				l.linkBody(s.Arms[i].Body)
			}
			l.linkBody(s.Else)
		case *Match:
			l.linkValue(&s.Scrutinee)
			for i := range s.Arms {
				l.linkType(&s.Arms[i].Type)
				l.linkBody(s.Arms[i].Body)
			}
			l.linkBody(s.Else)
		case *If:
			l.linkIf(s)
		case *For:
			l.linkType(&s.VarType)
			l.linkValue(&s.Iter)
			l.linkBody(s.Body)
		case *AssertStmt:
			l.linkValue(&s.Cond)
		default:
			panic(fmt.Sprintf("ir: link: unhandled Stmt %T", s))
		}
	}
}

// linkIf relinks an if and its else-if chain.
func (l *linker) linkIf(s *If) {
	if s == nil {
		return
	}
	l.linkValue(&s.Cond)
	l.linkBody(s.Then)
	l.linkIf(s.ElseIf)
	l.linkBody(s.Else)
}

// linkValue relinks one value node in place: its reference edges, its settled
// type, and its operands. The switch is exhaustive over the sealed Value
// forms; a new form panics rather than silently keeping its placeholders.
//
// every case is the form's edge list, so the length is the case count, not
// control complexity (the Lexer.Next class of exception).
//
//nolint:funlen // a flat exhaustive dispatch over the 25 sealed Value forms:
func (l *linker) linkValue(p *Value) {
	switch v := (*p).(type) {
	case nil:
	case *Adapt:
		l.linkValue(&v.Value)
		l.linkType(&v.To)
	case *IntLiteral:
		l.linkType(&v.Type)
	case *StringLiteral:
		l.linkType(&v.Type)
	case *BoolLiteral:
		l.linkType(&v.Type)
	case *DatetimeLiteral:
		l.linkType(&v.Type)
	case *DurationLiteral:
		l.linkType(&v.Type)
	case *CollectionLiteral:
		for i := range v.Entries {
			l.linkValue(&v.Entries[i].Key)
			l.linkValue(&v.Entries[i].Value)
		}
		l.linkType(&v.Type)
	case *RecordValue:
		for i := range v.Fields {
			l.linkValue(&v.Fields[i].Value)
		}
		l.linkType(&v.Type)
	case *Reference:
		l.resolveConst(&v.Target)
		l.linkType(&v.Type)
	case *Call:
		l.linkValue(&v.Receiver)
		l.linkArgs(v.Args)
		l.resolveMethod(&v.Resolved)
		l.linkSubst(v.Subst)
		l.linkType(&v.Type)
	case *FuncCall:
		l.resolveFunction(&v.Target)
		l.linkArgs(v.Args)
		l.resolveFunction(&v.Resolved)
		l.linkSubst(v.Subst)
		l.linkType(&v.Type)
	case *StaticCall:
		l.resolveTypeDef(&v.Def)
		l.linkArgs(v.Args)
		l.resolveMethod(&v.Resolved)
		l.linkSubst(v.Subst)
		l.linkType(&v.Type)
	case *Apply:
		l.linkValue(&v.Callee)
		l.linkArgs(v.Args)
		l.linkType(&v.Type)
	case *FuncLiteral:
		l.linkBody(v.Body)
		l.linkType(&v.Type)
	case *SelfValue:
		l.linkType(&v.Type)
	case *ParamRef:
		l.linkType(&v.Type)
	case *LocalRef:
		l.linkType(&v.Type)
	case *FieldAccess:
		l.linkValue(&v.Receiver)
		l.linkType(&v.Type)
	case *Conversion:
		l.linkType(&v.Type)
		l.linkArgs(v.Args)
	case *Await:
		l.linkValue(&v.Value)
		l.linkType(&v.Type)
	case *Ternary:
		l.linkValue(&v.Cond)
		l.linkValue(&v.Then)
		l.linkValue(&v.Else)
		l.linkType(&v.Type)
	case *RangeLit:
		l.linkValue(&v.Lower)
		l.linkValue(&v.Upper)
		l.linkType(&v.Type)
	case *NullValue:
		l.linkType(&v.Type)
	case *EnumMemberValue:
		l.resolveTypeDef(&v.Def)
		l.linkType(&v.Type)
	case *AssocConstValue:
		l.resolveTypeDef(&v.Def)
		l.linkType(&v.Type)
	case *TypeValue:
		l.linkType(&v.Reified)
		l.linkType(&v.Type)
	default:
		panic(fmt.Sprintf("ir: link: unhandled Value %T", v))
	}
}

// linkArgs relinks an argument list in place — the operand shorthand the
// call-shaped arms share.
func (l *linker) linkArgs(args []Value) {
	for i := range args {
		l.linkValue(&args[i])
	}
}

// linkSubst relinks a call's type-variable solution.
func (l *linker) linkSubst(subst map[string]Type) {
	for name, t := range subst {
		l.linkType(&t)
		subst[name] = t
	}
}

// linkType relinks one type in place: the reference edges of its nominal
// forms and the structural members of its composites.
func (l *linker) linkType(p *Type) {
	switch t := (*p).(type) {
	case nil, *Builtin, *invalid, *SelfType:
	case *Named:
		l.resolveTypeDef(&t.Def)
	case *Union:
		for i := range t.Members {
			l.linkType(&t.Members[i])
		}
	case *Record:
		for i := range t.Fields {
			l.linkType(&t.Fields[i].Type)
		}
	case *Func:
		for i := range t.Params {
			l.linkType(&t.Params[i])
		}
		l.linkType(&t.Result)
	case *TypeVar:
		l.linkType(&t.Bound)
	case *App:
		l.resolveTypeDef(&t.Def)
		for i := range t.Args {
			l.linkType(&t.Args[i])
		}
	default:
		panic(fmt.Sprintf("ir: link: unhandled Type %T", t))
	}
}

// linkConstant relinks a folded constant: its enum definition, its union tag,
// and its composite payloads.
func (l *linker) linkConstant(c *Constant) {
	if c == nil {
		return
	}
	l.resolveTypeDef(&c.EnumDef)
	l.linkType(&c.UnionTag)
	for i := range c.Coll {
		l.linkConstant(c.Coll[i].Key)
		l.linkConstant(c.Coll[i].Value)
	}
	for i := range c.Fields {
		l.linkConstant(c.Fields[i].Value)
	}
	if c.Fn != nil {
		var v Value = c.Fn
		l.linkValue(&v)
	}
	for _, captured := range c.Captured {
		l.linkConstant(captured)
	}
}
