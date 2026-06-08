package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/internal/version"
)

func init() {
	RootCmd.AddCommand(VersionCmd)
	// Setting RootCmd.Version makes `masterbelt --version` print the same string
	// cobra resolves it from, so the flag and the subcommand never disagree.
	RootCmd.Version = version.String()
}

// VersionCmd prints the build's identity: its version and channel, the commit
// it was built from and that commit's date, and the Go toolchain and target.
// --reporter=json emits the same facts in the machine-readable shape the other
// JSON surfaces (check --reporter=json, --stats) use.
var VersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the masterbelt version and build metadata",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		info := version.Get()
		out := cmd.OutOrStdout()
		switch kind, _ := cmd.Flags().GetString("reporter"); kind {
		case reporterText:
			lines := []string{fmt.Sprintf("masterbelt %s (%s)", info.Version, info.Channel)}
			if info.Commit != "" {
				lines = append(lines, "  commit:  "+info.Commit)
			}
			if info.Date != "" {
				lines = append(lines, "  date:    "+info.Date)
			}
			lines = append(lines, "  go:      "+info.Go, "  os/arch: "+info.OS+"/"+info.Arch)
			_, err := io.WriteString(out, strings.Join(lines, "\n")+"\n")
			return err
		case reporterJSON:
			doc, err := json.MarshalIndent(info, "", "  ")
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(out, string(doc))
			return err
		default:
			return fmt.Errorf("unknown reporter %q (want text or json)", kind)
		}
	},
}
