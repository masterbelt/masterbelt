// This file is the shared test harness for the semantic analyzer: the analyze
// driver and the codes/hasCode diagnostic helpers every analyze_*_test.go uses.
package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

func analyze(src string) (*ir.Module, []diagnostic.Diagnostic) {
	return Analyze(abstract.NewDocument([]byte(src)))
}

func codes(diags []diagnostic.Diagnostic) []diagnostic.Code {
	out := make([]diagnostic.Code, len(diags))
	for i, d := range diags {
		out[i] = d.Code
	}
	return out
}

func hasCode(diags []diagnostic.Diagnostic, code diagnostic.Code) bool {
	for _, c := range codes(diags) {
		if c == code {
			return true
		}
	}
	return false
}
