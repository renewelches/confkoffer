package cmd

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
		Use:          "list",
		Short:        "List snapshots under <name>/, newest first.",
		SilenceUsage: true,
		RunE:         runList,
	}
	c.Flags().String("name", "", "project name / S3 prefix (env CONFKOFFER_NAME)")
	c.Flags().String("bucket", "", "S3 bucket (env CONFKOFFER_BUCKET, default 'confkoffer')")
	c.Flags().String("endpoint", "", "S3 endpoint (env AWS_ENDPOINT)")
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

	cli, err := store.New(store.Config{
		Bucket:   cfg.Storage.Bucket,
		Endpoint: cfg.Storage.Endpoint,
		Region:   cfg.Storage.Region,
	})
	if err != nil {
		return err
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
