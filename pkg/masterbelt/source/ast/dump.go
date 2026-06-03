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
	case *BoolLit:
		return fmt.Sprintf("BoolLit %v", x.Value)
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
