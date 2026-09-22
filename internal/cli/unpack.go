package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/renewelches/confkoffer/internal/archive"
	"github.com/renewelches/confkoffer/internal/config"
	"github.com/renewelches/confkoffer/internal/crypto"
	"github.com/renewelches/confkoffer/internal/store"
)

func addUnpack(root *cobra.Command) {
	c := &cobra.Command{
		Use:   "unpack",
		Short: "Download, decrypt, and extract the latest (or selected) snapshot.",
		Long: `Download a snapshot, decrypt it, and extract it into --output-dir.

Selection is exactly one of: the newest snapshot under <name>/ (the
default), --object-key for an exact key, or --at for the newest snapshot
at-or-before a timestamp.

"Newest" and --at are both judged by the store's LastModified, not by
the timestamp in the key. The key records when a snapshot was packed and
travels with the bytes; LastModified records when this store last wrote
it. They normally agree, and diverge if objects are copied, synced, or
restored from a lifecycle tier — in which case the store's own view
wins.`,
		SilenceUsage: true,
		RunE:         runUnpack,
	}
	c.Flags().String("name", "", "project name; also the key prefix within the store (env CONFKOFFER_NAME)")
	c.Flags().String("bucket", "", "S3 bucket, for the aws/s3/minio providers (env CONFKOFFER_BUCKET, default 'confkoffer')")
	c.Flags().String("endpoint", "", "S3 endpoint, for the aws/s3/minio providers (env AWS_ENDPOINT)")
	c.Flags().String("pass", "", "passphrase value (env CONFKOFFER_PASS)")
	c.Flags().String("output-dir", ".", "directory to extract into")
	c.Flags().Bool("overwrite", false, "overwrite existing files in output-dir")
	c.Flags().String("object-key", "", "exact object key to fetch (skips list)")
	c.Flags().String("at", "", "fetch the newest snapshot at-or-before this RFC3339 timestamp (compared against the store's LastModified, not the key)")
	root.AddCommand(c)
}

func runUnpack(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	ov := config.Overrides{
		Name:     stringFlag(cmd, "name"),
		Bucket:   stringFlag(cmd, "bucket"),
		Endpoint: stringFlag(cmd, "endpoint"),
		Password: stringFlag(cmd, "pass"),
	}
	cfg, err := loadAndResolveConfig(cmd, ov)
	if err != nil {
		return err
	}

	objectKey, _ := cmd.Flags().GetString("object-key")
	atStr, _ := cmd.Flags().GetString("at")
	if objectKey != "" && atStr != "" {
		return configError{errors.New("--object-key and --at are mutually exclusive")}
	}

	// store.New runs the provider's shape validation (endpoint scheme,
	// absolute dirpath, container name), so its failures are config
	// errors and must land on exit code 2 alongside the missing-field
	// checks in Resolve — not on 1, which means a runtime failure.
	cli, err := store.New(cfg.Storage.BlobConfig)
	if err != nil {
		return configError{err}
	}

	key, err := pickKey(ctx, cli, cfg.Name, objectKey, atStr)
	if err != nil {
		return err
	}

	blob, err := cli.Get(ctx, key)
	if err != nil {
		return err
	}

	src, err := buildPasswordSource(cfg, false /* no confirm */)
	if err != nil {
		return configError{err}
	}
	pw, err := src.Get(ctx)
	if err != nil {
		return err
	}
	defer wipe(pw)

	plaintext, err := crypto.Decrypt(blob, pw)
	if err != nil {
		return err
	}
	defer wipe(plaintext)

	outputDir, _ := cmd.Flags().GetString("output-dir")
	overwrite, _ := cmd.Flags().GetBool("overwrite")
	res, err := archive.Unpack(plaintext, outputDir, overwrite)
	if err != nil {
		return err
	}

	slog.Info("unpack: done", "key", key, "written", len(res.Written), "skipped", len(res.Skipped))
	for _, p := range res.Skipped {
		fmt.Fprintf(os.Stdout, "skipped (exists): %s\n", p)
	}
	fmt.Fprintf(os.Stdout, "extracted %d file(s) into %s (%d skipped)\n", len(res.Written), outputDir, len(res.Skipped))
	return nil
}

// pickKey takes store.Storage rather than *store.BlobClient so the
// selection logic can be exercised against a fake, and so the interface
// has at least one consumer keeping it honest.
func pickKey(ctx context.Context, cli store.Storage, name, objectKey, atStr string) (string, error) {
	if objectKey != "" {
		return objectKey, nil
	}
	if atStr != "" {
		t, err := time.Parse(time.RFC3339, atStr)
		if err != nil {
			return "", configError{fmt.Errorf("--at must be RFC3339 (e.g. 2026-04-28T12:00:00Z): %w", err)}
		}
		objs, err := cli.List(ctx, name)
		if err != nil {
			return "", err
		}
		obj, err := store.PickAt(objs, t)
		if err != nil {
			return "", err
		}
		return obj.Key, nil
	}
	objs, err := cli.List(ctx, name)
	if err != nil {
		return "", err
	}
	return objs[0].Key, nil // newest
}
