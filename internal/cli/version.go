package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/renewelches/confkoffer/internal/version"
)

func addVersion(root *cobra.Command) {
	c := &cobra.Command{
		Use:          "version",
		Short:        "Print the confkoffer version, commit, and build date.",
		Long: `Print the confkoffer version, commit, and build date.

Useful for confirming which release a binary was built from, especially
when troubleshooting or comparing against the SLSA provenance attached
to a GitHub release.`,
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "confkoffer %s (commit %s, built %s)\n",
				version.Version, version.Commit, version.Date)
		},
	}
	root.AddCommand(c)
}
