package cmd

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
		Use:          "unpack",
		Short:        "Download, decrypt, and extract the latest (or selected) snapshot.",
		SilenceUsage: true,
		RunE:         runUnpack,
	}
	c.Flags().String("name", "", "project name / S3 prefix (env CONFKOFFER_NAME)")
	c.Flags().String("bucket", "", "S3 bucket (env CONFKOFFER_BUCKET, default 'confkoffer')")
	c.Flags().String("endpoint", "", "S3 endpoint (env AWS_ENDPOINT)")
	c.Flags().String("pass", "", "passphrase value (env CONFKOFFER_PASS)")
	c.Flags().String("output-dir", ".", "directory to extract into")
	c.Flags().Bool("overwrite", false, "overwrite existing files in output-dir")
	c.Flags().String("object-key", "", "exact S3 object key to fetch (skips list)")
	c.Flags().String("at", "", "fetch the newest snapshot at-or-before this RFC3339 timestamp")
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

	cli, err := store.New(store.Config{
		Bucket:   cfg.Storage.Bucket,
		Endpoint: cfg.Storage.Endpoint,
		Region:   cfg.Storage.Region,
	})
	if err != nil {
		return err
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

func pickKey(ctx context.Context, cli *store.Client, name, objectKey, atStr string) (string, error) {
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
