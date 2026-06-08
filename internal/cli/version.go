package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/masterbelt/masterbelt/internal/version"
)

func init() {
	RootCmd.AddCommand(VersionCmd)
	VersionCmd.Flags().String("format", "text", "output format: text or json")
	// Setting RootCmd.Version makes `masterbelt --version` print the same string
	// cobra resolves it from, so the flag and the subcommand never disagree.
	RootCmd.Version = version.String()
}

// VersionCmd prints the build's identity: its version and channel, the commit
// it was built from and that commit's date, and the Go toolchain and target.
// --format=json emits the same facts in the machine-readable shape the other
// JSON surfaces (check --format=json, --stats) use.
var VersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the masterbelt version and build metadata",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		info := version.Get()
		out := cmd.OutOrStdout()
		switch format, _ := cmd.Flags().GetString("format"); format {
		case "text":
			fmt.Fprintf(out, "masterbelt %s (%s)\n", info.Version, info.Channel)
			if info.Commit != "" {
				fmt.Fprintf(out, "  commit:  %s\n", info.Commit)
			}
			if info.Date != "" {
				fmt.Fprintf(out, "  date:    %s\n", info.Date)
			}
			fmt.Fprintf(out, "  go:      %s\n", info.Go)
			fmt.Fprintf(out, "  os/arch: %s/%s\n", info.OS, info.Arch)
			return nil
		case "json":
			doc, err := json.MarshalIndent(info, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(out, string(doc))
			return nil
		default:
			return fmt.Errorf("unknown format %q (want text or json)", format)
		}
	},
}
