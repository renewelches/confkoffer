package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"confkoffer/internal/archive"
	"confkoffer/internal/config"
	"confkoffer/internal/crypto"
	"confkoffer/internal/scan"
	"confkoffer/internal/store"
)

func addPack(root *cobra.Command) {
	c := &cobra.Command{
		Use:          "pack",
		Short:        "Bundle, encrypt, and upload the matched files.",
		SilenceUsage: true,
		RunE:         runPack,
	}
	c.Flags().String("name", "", "project name / S3 prefix (env CONFKOFFER_NAME)")
	c.Flags().String("bucket", "", "S3 bucket (env CONFKOFFER_BUCKET, default 'confkoffer')")
	c.Flags().String("endpoint", "", "S3 endpoint (env AWS_ENDPOINT)")
	c.Flags().String("pass", "", "passphrase value (env CONFKOFFER_PASS) — avoid for automation; use pass/command source")
	c.Flags().String("source-dir", ".", "directory to scan for files to pack")
	root.AddCommand(c)
}

func runPack(cmd *cobra.Command, _ []string) error {
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
	if len(cfg.Patterns.Include) == 0 {
		return configError{fmt.Errorf("patterns.include is empty — nothing would be packed")}
	}

	sourceDir, _ := cmd.Flags().GetString("source-dir")
	matches, err := scan.Walk(sourceDir, scan.Patterns{
		Include: cfg.Patterns.Include,
		Exclude: cfg.Patterns.Exclude,
	})
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("no files matched include patterns under %s", sourceDir)
	}

	plaintext, err := buildArchive(matches)
	if err != nil {
		return err
	}

	src, err := buildPasswordSource(cfg, true /* confirm */)
	if err != nil {
		return configError{err}
	}
	pw, err := src.Get(ctx)
	if err != nil {
		return err
	}
	defer wipe(pw)

	blob, err := crypto.Encrypt(plaintext, pw, cfg.Crypto.Argon2id)
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

	key, err := store.KeyForName(cfg.Name)
	if err != nil {
		return err
	}
	if err := cli.Put(ctx, key, blob); err != nil {
		return err
	}
	slog.Info("pack: uploaded",
		"key", key,
		"bytes", len(blob),
		"files", len(matches),
		"bucket", cfg.Storage.Bucket,
	)
	fmt.Fprintf(os.Stdout, "uploaded %s/%s (%d bytes, %d files)\n", cfg.Storage.Bucket, key, len(blob), len(matches))
	return nil
}

func buildArchive(matches []scan.Match) ([]byte, error) {
	w := archive.NewWriter()
	for _, m := range matches {
		f, err := os.Open(m.AbsPath)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", m.RelPath, err)
		}
		err = w.Add(m.RelPath, m.Mode.Perm(), f)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	return w.Bytes()
}

// wipe is a small alias so call sites read clearly.
func wipe(b []byte) { crypto.Zero(b) }
