package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/diagnostic/reporter"
)

// reporterText and reporterJSON are the --reporter flag values shared by the
// subcommands that offer one (check, ir, version).
const (
	reporterText = "text"
	reporterJSON = "json"
)

// newReporter builds the diagnostic reporter for the run's --reporter, writing
// to out. It is shared by every command that reports diagnostics, so
// --reporter=json gives JSON diagnostics everywhere — text streams lines, json
// emits one document on Flush. The locale comes from --locale when the command
// defines it, otherwise the default.
func newReporter(cmd *cobra.Command, out io.Writer) (reporter.Reporter, error) {
	locale := diagnostic.DefaultLocale
	if l, _ := cmd.Flags().GetString("locale"); l != "" {
		locale = diagnostic.Locale(l)
	}
	switch kind, _ := cmd.Flags().GetString("reporter"); kind {
	case reporterText:
		return reporter.NewText(out, locale), nil
	case reporterJSON:
		return reporter.NewJSON(out, locale), nil
	default:
		return nil, fmt.Errorf("unknown reporter %q (want text or json)", kind)
	}
}
