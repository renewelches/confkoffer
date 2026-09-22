package cli

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/renewelches/confkoffer/internal/config"
	"github.com/renewelches/confkoffer/internal/store"
)

func addList(root *cobra.Command) {
	c := &cobra.Command{
		Use:   "list",
		Short: "List snapshots under <name>/, newest first.",
		Long: `List the snapshots stored under <name>/, newest first.

Ordering is by the store's LastModified, which is also what unpack
treats as authoritative. The timestamp inside each key is a
human-readable label only.`,
		SilenceUsage: true,
		RunE:         runList,
	}
	c.Flags().String("name", "", "project name; also the key prefix within the store (env CONFKOFFER_NAME)")
	c.Flags().String("bucket", "", "S3 bucket, for the aws/s3/minio providers (env CONFKOFFER_BUCKET, default 'confkoffer')")
	c.Flags().String("endpoint", "", "S3 endpoint, for the aws/s3/minio providers (env AWS_ENDPOINT)")
	root.AddCommand(c)
}

func runList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	ov := config.Overrides{
		Name:     stringFlag(cmd, "name"),
		Bucket:   stringFlag(cmd, "bucket"),
		Endpoint: stringFlag(cmd, "endpoint"),
	}
	cfg, err := loadAndResolveConfig(cmd, ov)
	if err != nil {
		return err
	}

	// store.New runs the provider's shape validation (endpoint scheme,
	// absolute dirpath, container name), so its failures are config
	// errors and must land on exit code 2 alongside the missing-field
	// checks in Resolve — not on 1, which means a runtime failure.
	cli, err := store.New(cfg.Storage.BlobConfig)
	if err != nil {
		return configError{err}
	}

	objs, err := cli.List(ctx, cfg.Name)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LAST_MODIFIED\tSIZE\tKEY")
	for _, o := range objs {
		fmt.Fprintf(tw, "%s\t%d\t%s\n",
			o.LastModified.UTC().Format(time.RFC3339),
			o.Size,
			o.Key,
		)
	}
	return tw.Flush()
}
