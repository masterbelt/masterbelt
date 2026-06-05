package ast

import (
	"fmt"
	"strings"
)

// Dump renders a File as a stable, diffable text tree. It reads only the
// resolved fields (no positions, no buffer), so two Files dump to the same text
// exactly when they are structurally equal — which makes Dump both the snapshot
// format and the oracle an incremental lowering is checked against.
func Dump(f *File) string {
	var b strings.Builder
	b.WriteString("File\n")
	for _, d := range f.Uses {
		dumpUseDecl(&b, d)
	}
	for _, d := range f.Decls {
		dumpConstDecl(&b, d)
	}
	for _, d := range f.Types {
		dumpTypeDecl(&b, d)
	}
	for _, d := range f.Funcs {
		dumpFuncDecl(&b, d)
	}
	for _, d := range f.Asserts {
		dumpAssertDecl(&b, d)
	}
	return b.String()
}

func dumpUseDecl(b *strings.Builder, d *UseDecl) {
	b.WriteString("  UseDecl\n")
	if d.Public {
		b.WriteString("    pub\n")
	}
	switch {
	case d.Star:
		b.WriteString("    target *\n")
	case d.Namespace != "":
		fmt.Fprintf(b, "    namespace %q\n", d.Namespace)
	default:
		for _, n := range d.Names {
			fmt.Fprintf(b, "    name %q\n", n)
		}
	}
	fmt.Fprintf(b, "    from %q\n", d.Path)
}

func dumpConstDecl(b *strings.Builder, d *ConstDecl) {
	b.WriteString("  ConstDecl\n")
	for _, doc := range d.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	if d.Public {
		b.WriteString("    pub\n")
	}
	fmt.Fprintf(b, "    name %q\n", d.Name)
	if d.Type != nil {
		fmt.Fprintf(b, "    type %s\n", dumpType(d.Type))
	}
	if d.Value != nil {
		fmt.Fprintf(b, "    value %s\n", dumpExpr(d.Value))
	}
}

func dumpFuncDecl(b *strings.Builder, d *FuncDecl) {
	b.WriteString("  FuncDecl\n")
	for _, doc := range d.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	if d.Public {
		b.WriteString("    pub\n")
	}
	if d.Extern {
		b.WriteString("    extern\n")
	}
	if len(d.Effects) > 0 {
		fmt.Fprintf(b, "    effects %s\n", strings.Join(d.Effects, " "))
	}
	fmt.Fprintf(b, "    name %q\n", d.Name)
	for _, p := range d.Params {
		fmt.Fprintf(b, "    param %s: %s\n", p.Name, dumpType(p.Type))
	}
	if d.Result != nil {
		fmt.Fprintf(b, "    result %s\n", dumpType(d.Result))
	}
	for _, s := range d.Body {
		dumpStmt(b, s)
	}
}

func dumpAssertDecl(b *strings.Builder, d *AssertDecl) {
	b.WriteString("  AssertDecl\n")
	for _, doc := range d.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	fmt.Fprintf(b, "    cond %s\n", dumpExpr(d.Cond))
}

func dumpExpr(e Expr) string {
	switch x := e.(type) {
	case nil:
		return "<missing>"
	case *IntLit:
		return fmt.Sprintf("IntLit %q", x.Text)
	case *StringLit:
		return fmt.Sprintf("StringLit %q", x.Value)
	case *DatetimeLit:
		return fmt.Sprintf("DatetimeLit %q", x.Text)
	case *DurationLit:
		return fmt.Sprintf("DurationLit %q", x.Text)
	case *BoolLit:
		return fmt.Sprintf("BoolLit %v", x.Value)
	case *NullLit:
		return "NullLit"
	case *CollectionLit:
		label := "collection"
		if x.IsMap() {
			label = "map"
		} else if len(x.Entries) > 0 {
			label = "list"
		}
		parts := []string{label}
		for _, e := range x.Entries {
			if e.Key != nil {
				parts = append(parts, dumpExpr(e.Key)+": "+dumpExpr(e.Value))
			} else {
				parts = append(parts, dumpExpr(e.Value))
			}
		}
		return "(" + strings.Join(parts, " ") + ")"
	case *RecordLit:
		label := "record"
		if x.TypeName != "" {
			label = "record " + x.TypeName
		}
		parts := []string{label}
		for _, f := range x.Fields {
			parts = append(parts, f.Name+": "+dumpExpr(f.Value))
		}
		return "(" + strings.Join(parts, " ") + ")"
	case *SelfExpr:
		return "self"
	case *Identifier:
		return fmt.Sprintf("Identifier %q", x.Name)
	case *MemberExpr:
		return fmt.Sprintf("(. %s %s)", dumpExpr(x.Receiver), x.Member.Name)
	case *CallExpr:
		parts := []string{"call", dumpExpr(x.Callee)}
		for _, a := range x.Arguments {
			parts = append(parts, dumpExpr(a))
		}
		return "(" + strings.Join(parts, " ") + ")"
	case *AwaitExpr:
		return "(await " + dumpExpr(x.Value) + ")"
	case *FuncLit:
		params := make([]string, len(x.Params))
		for i, p := range x.Params {
			params[i] = p.Name + ": " + dumpType(p.Type)
		}
		parts := []string{"fn(" + strings.Join(params, ", ") + "): " + dumpType(x.Result)}
		for _, s := range x.Body {
			parts = append(parts, dumpStmtInline(s))
		}
		return "(" + strings.Join(parts, " ") + ")"
	default:
		return "Expr(?)"
	}
}

// dumpStmtInline renders a statement compactly on one line, for a function
// literal dumped as part of an enclosing expression. (dumpStmt is the
// multi-line, indented form used for a method body.)
func dumpStmtInline(s Stmt) string {
	switch s := s.(type) {
	case *ReturnStmt:
		return "(return " + dumpExpr(s.Value) + ")"
	case *ExprStmt:
		return "(expr " + dumpExpr(s.X) + ")"
	default:
		return ""
	}
}

func dumpTypeDecl(b *strings.Builder, d *TypeDecl) {
	b.WriteString("  TypeDecl\n")
	for _, doc := range d.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	if d.Public {
		b.WriteString("    pub\n")
	}
	fmt.Fprintf(b, "    name %q\n", d.Name)
	for _, p := range d.Params {
		if p.Constraint != nil {
			fmt.Fprintf(b, "    param %s: %s\n", p.Name, dumpType(p.Constraint))
		} else {
			fmt.Fprintf(b, "    param %s\n", p.Name)
		}
	}
	if d.Body != nil {
		fmt.Fprintf(b, "    body %s\n", dumpType(d.Body))
	}
	if d.Where != nil {
		fmt.Fprintf(b, "    where %s\n", dumpExpr(d.Where))
	}
	for _, m := range d.Methods {
		dumpMethod(b, m)
	}
}

func dumpMethod(b *strings.Builder, m *MethodDecl) {
	fmt.Fprintf(b, "    method %q\n", m.Name)
	for _, doc := range m.Doc {
		fmt.Fprintf(b, "      doc %q\n", doc)
	}
	if m.Public {
		b.WriteString("      pub\n")
	}
	if m.Extern {
		b.WriteString("      extern\n")
	}
	if len(m.Effects) > 0 {
		fmt.Fprintf(b, "      effects %s\n", strings.Join(m.Effects, " "))
	}
	for _, p := range m.Params {
		fmt.Fprintf(b, "      param %s: %s\n", p.Name, dumpType(p.Type))
	}
	if m.Result != nil {
		fmt.Fprintf(b, "      result %s\n", dumpType(m.Result))
	}
	for _, s := range m.Body {
		dumpStmt(b, s)
	}
}

func dumpStmt(b *strings.Builder, s Stmt) {
	switch s := s.(type) {
	case *ReturnStmt:
		fmt.Fprintf(b, "      return %s\n", dumpExpr(s.Value))
	case *ExprStmt:
		fmt.Fprintf(b, "      expr %s\n", dumpExpr(s.X))
	}
}

// dumpType renders a type expression in a source-like form: int8, Optional<T>,
// A | B, { id: int8 }, fn(src: T): R.
func dumpType(t TypeExpr) string {
	switch t := t.(type) {
	case nil:
		return "<missing>"
	case *NamedType:
		name := t.Name
		if t.Namespace != "" {
			name = t.Namespace + "." + t.Name
		}
		if len(t.Args) == 0 {
			return name
		}
		args := make([]string, len(t.Args))
		for i, a := range t.Args {
			args[i] = dumpType(a)
		}
		return name + "<" + strings.Join(args, ", ") + ">"
	case *UnionType:
		parts := make([]string, len(t.Members))
		for i, m := range t.Members {
			parts[i] = dumpType(m)
		}
		return strings.Join(parts, " | ")
	case *RecordType:
		parts := make([]string, len(t.Fields))
		for i, f := range t.Fields {
			parts[i] = f.Name + ": " + dumpType(f.Type)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case *FuncType:
		params := make([]string, len(t.Params))
		for i, p := range t.Params {
			params[i] = p.Name + ": " + dumpType(p.Type)
		}
		return "fn(" + strings.Join(params, ", ") + "): " + dumpType(t.Result)
	case *BuiltinType:
		if len(t.Args) == 0 {
			return "builtin"
		}
		args := make([]string, len(t.Args))
		for i, a := range t.Args {
			args[i] = dumpType(a)
		}
		return "builtin<" + strings.Join(args, ", ") + ">"
	default:
		return "Type(?)"
	}
}
