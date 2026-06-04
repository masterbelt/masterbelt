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
	for _, d := range f.Decls {
		dumpConstDecl(&b, d)
	}
	for _, d := range f.Types {
		dumpTypeDecl(&b, d)
	}
	return b.String()
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
		fmt.Fprintf(b, "    type %q\n", d.Type.Name)
	}
	if d.Value != nil {
		fmt.Fprintf(b, "    value %s\n", dumpExpr(d.Value))
	}
}

func dumpExpr(e Expr) string {
	switch x := e.(type) {
	case nil:
		return "<missing>"
	case *IntLit:
		return fmt.Sprintf("IntLit %q", x.Text)
	case *StringLit:
		return fmt.Sprintf("StringLit %q", x.Value)
	case *BoolLit:
		return fmt.Sprintf("BoolLit %v", x.Value)
	case *NullLit:
		return "NullLit"
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
	default:
		return "Expr(?)"
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
	for _, m := range d.Methods {
		dumpMethod(b, m)
	}
}

func dumpMethod(b *strings.Builder, m *MethodDecl) {
	fmt.Fprintf(b, "    method %q\n", m.Name)
	if m.Public {
		b.WriteString("      pub\n")
	}
	if m.Extern {
		b.WriteString("      extern\n")
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
		if len(t.Args) == 0 {
			return t.Name
		}
		args := make([]string, len(t.Args))
		for i, a := range t.Args {
			args[i] = dumpType(a)
		}
		return t.Name + "<" + strings.Join(args, ", ") + ">"
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
