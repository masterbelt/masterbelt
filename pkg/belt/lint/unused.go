package lint

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// unusedDeclarations reports private declarations no root reaches — a mark-and-
// sweep over the module's reference graph. The roots (every public declaration
// and every assert) are live; a declaration is live when a live one references
// it. An unreached private declaration is dead, even one in a reference cycle
// that a plain reference count would miss (two private declarations that name
// only each other are both dead).
//
// The marking is comprehensive — it follows references through constants,
// functions, methods, types, where-predicates, asserts, associated constants,
// and enum members — so a declaration used anywhere reachable stays live. The
// reporting covers top-level constants, functions, and types (enums and
// interfaces are types): the named declarations a name can reach. A method or
// an associated constant is not reported — a reached type's are marked whole,
// since which a caller invokes is a dispatch question the lint does not decide.
// A public declaration is never reported: the public surface is live by
// definition, used or not.
func (l *linter) unusedDeclarations(m *ir.Module) {
	mk := newMarker()
	mk.seedRoots(m)
	for _, c := range m.Consts {
		if c != nil && c.Syntax != nil && !c.Public && !mk.consts[c] {
			l.reportUnused(c.Syntax, c.Name)
		}
	}
	for _, f := range m.Funcs {
		if f != nil && f.Syntax != nil && !f.Public && !mk.funcs[f] {
			l.reportUnused(f.Syntax, f.Name)
		}
	}
	for _, t := range m.Types {
		if t != nil && !t.Public && !mk.types[t] {
			// DeclSyntax reads the declaration backpointer for whichever kind t is
			// — a type, enum, interface, or master (a master keeps it on
			// MasterSyntax, Body being nil for it) — so an unused private one of any
			// kind is anchored and reported the same way.
			l.reportUnused(t.DeclSyntax(), t.Name)
		}
	}
}

// reportUnused emits the unused-declaration diagnostic for a named declaration
// at syntax, unless it is unnamed, unanchored, or already covered by an error.
func (l *linter) reportUnused(syntax ast.Node, name string) {
	if name == "" || syntax == nil {
		return
	}
	off, width := l.span(syntax)
	if width > 0 && !l.brokenWithin(off, width) {
		l.diags = append(l.diags, unusedDeclaration(off, width, name))
	}
}

// marker is the live set of a mark-and-sweep: the declarations reached from the
// roots. Each reach method marks its declaration and walks its definition for
// more, so the closure is complete once the seeds return; the visited maps make
// a reference cycle terminate.
type marker struct {
	consts  map[*ir.Const]bool
	funcs   map[*ir.Function]bool
	methods map[*ir.Method]bool
	types   map[*ir.TypeDef]bool
}

func newMarker() *marker {
	return &marker{
		consts:  map[*ir.Const]bool{},
		funcs:   map[*ir.Function]bool{},
		methods: map[*ir.Method]bool{},
		types:   map[*ir.TypeDef]bool{},
	}
}

// seedRoots marks the live roots: every public declaration (the surface an
// importer reaches) and every assert (the compile-time tests exercise what they
// read). A private declaration is live only if reached from one of these.
func (mk *marker) seedRoots(m *ir.Module) {
	for _, c := range m.Consts {
		if c.Public {
			mk.reachConst(c)
		}
	}
	for _, f := range m.Funcs {
		if f.Public {
			mk.reachFunc(f)
		}
	}
	for _, t := range m.Types {
		if t.Public {
			mk.reachType(t)
		}
	}
	for _, a := range m.Asserts {
		mk.walkValue(a.CondGraph)
	}
}

// visit is the value-node hook: every reference edge a node carries reaches its
// target, and the node's settled type reaches the definitions it names. It is
// the one place the tree:"ref" edges are followed — a ref-carrying value form
// the marker forgets here would let a live declaration read as dead, which
// TestRefEdgeCoverage guards against.
func (mk *marker) visit(n ir.Value) bool {
	switch n := n.(type) {
	case *ir.Reference:
		mk.reachConst(n.Target)
	case *ir.FuncCall:
		mk.reachFunc(n.Target)
		mk.reachFunc(n.Resolved)
	case *ir.Call:
		mk.reachMethod(n.Resolved)
	case *ir.StaticCall:
		mk.reachType(n.Def)
		mk.reachMethod(n.Resolved)
	case *ir.EnumMemberValue:
		mk.reachType(n.Def)
	case *ir.AssocConstValue:
		mk.reachType(n.Def)
	}
	mk.reachTypeRef(ir.TypeOf(n))
	return true
}

func (mk *marker) walkValue(v ir.Value) { ir.WalkValues(v, mk.visit) }
func (mk *marker) walkBody(b []ir.Stmt) { ir.WalkBody(b, mk.visit) }

func (mk *marker) reachConst(c *ir.Const) {
	if c == nil || mk.consts[c] {
		return
	}
	mk.consts[c] = true
	mk.reachTypeRef(c.Type)
	mk.walkValue(c.Value)
}

func (mk *marker) reachFunc(f *ir.Function) {
	if f == nil || mk.funcs[f] {
		return
	}
	mk.funcs[f] = true
	mk.reachSignature(f.Params, f.Result)
	mk.walkBody(f.Body)
}

func (mk *marker) reachMethod(m *ir.Method) {
	if m == nil || mk.methods[m] {
		return
	}
	mk.methods[m] = true
	mk.reachType(m.Owner)
	mk.reachSignature(m.Params, m.Result)
	mk.walkBody(m.Body)
}

// reachType marks a type and everything its definition reaches. A reached type's
// methods are followed whole: which of them a caller invokes is a dispatch
// question the lint does not decide, so the conservative read keeps a constant a
// reachable type's method uses live rather than risk calling it dead.
func (mk *marker) reachType(t *ir.TypeDef) {
	if t == nil || mk.types[t] {
		return
	}
	mk.types[t] = true
	mk.reachTypeRef(t.Body)
	for _, im := range t.Impls {
		mk.reachTypeRef(im)
	}
	for _, p := range t.Params {
		mk.reachTypeRef(p.Bound)
	}
	mk.walkValue(t.Where)
	for _, ac := range t.Consts {
		mk.reachTypeRef(ac.Type)
		mk.walkValue(ac.ValueGraph)
	}
	if t.Enum != nil {
		for _, em := range t.Enum.Members {
			mk.walkValue(em.ValueGraph)
		}
	}
	// A master's row is a type on the descriptor rather than on Body, so reach it:
	// this keeps a named row alias live (record Row) and the types its fields name
	// (a field typed by an enum keeps that enum live).
	if t.Master != nil {
		mk.reachTypeRef(t.Master.Row)
		// The per-row validate checks read the row through value graphs, so a
		// declaration used only from one (a const a check compares against) is
		// reached here — otherwise it would read as unused.
		for _, c := range t.Master.RowChecks {
			if c != nil {
				mk.walkValue(c.Cond)
			}
		}
		// The per-table validate checks read the relation through value graphs the
		// same way, so a declaration used only from a validate all check (a const a
		// count is compared against) is reached too.
		for _, c := range t.Master.AllChecks {
			if c != nil {
				mk.walkValue(c.Cond)
			}
		}
	}
	for _, meth := range t.Methods {
		mk.reachMethod(meth)
	}
}

func (mk *marker) reachSignature(params []ir.Param, result ir.Type) {
	for _, p := range params {
		mk.reachTypeRef(p.Type)
	}
	mk.reachTypeRef(result)
}

// reachTypeRef reaches every type definition a type names, through composites —
// a const typed by a refinement keeps that type's where-predicate's constants
// live.
func (mk *marker) reachTypeRef(ty ir.Type) {
	switch ty := ty.(type) {
	case *ir.Named:
		mk.reachType(ty.Def)
	case *ir.App:
		mk.reachType(ty.Def)
		for _, a := range ty.Args {
			mk.reachTypeRef(a)
		}
	case *ir.Union:
		for _, mem := range ty.Members {
			mk.reachTypeRef(mem)
		}
	case *ir.Record:
		for _, f := range ty.Fields {
			mk.reachTypeRef(f.Type)
		}
	case *ir.Func:
		for _, p := range ty.Params {
			mk.reachTypeRef(p)
		}
		mk.reachTypeRef(ty.Result)
	case *ir.TypeVar:
		mk.reachTypeRef(ty.Bound)
	}
}

// unusedDeclaration builds the unused-declaration diagnostic, tagged Unnecessary
// so an editor fades the dead declaration rather than underlining it.
func unusedDeclaration(offset, width int, name string) diagnostic.Diagnostic {
	d := newUnusedDeclarationDiagnostic(offset, width, name)
	d.Tags = []diagnostic.Tag{diagnostic.TagUnnecessary}
	return d
}
