package cli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/renewelches/confkoffer/internal/archive"
	"github.com/renewelches/confkoffer/internal/config"
	"github.com/renewelches/confkoffer/internal/crypto"
	"github.com/renewelches/confkoffer/internal/scan"
	"github.com/renewelches/confkoffer/internal/store"
)

func addPack(root *cobra.Command) {
	c := &cobra.Command{
		Use:   "pack",
		Short: "Bundle, encrypt, and upload the matched files.",
		Long: `Bundle every file matching patterns.include, encrypt the bundle with
a passphrase-derived key, and upload it as a single object under
<name>/ in the configured store.`,
		SilenceUsage: true,
		RunE:         runPack,
	}
	c.Flags().String("name", "", "project name; also the key prefix within the store (env CONFKOFFER_NAME)")
	c.Flags().String("bucket", "", "S3 bucket, for the aws/s3/minio providers (env CONFKOFFER_BUCKET, default 'confkoffer')")
	c.Flags().String("endpoint", "", "S3 endpoint, for the aws/s3/minio providers (env AWS_ENDPOINT)")
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
	defer wipe(plaintext)

	// Build the storage client before prompting. store.New runs the
	// provider's shape validation (endpoint scheme, absolute dirpath,
	// container name), and a malformed endpoint should surface now, not
	// after the user has typed a passphrase twice and waited for
	// Argon2id. Those failures are config errors, so they land on exit
	// code 2 alongside Resolve's missing-field checks rather than on 1,
	// which means a runtime failure.
	cli, err := store.New(cfg.Storage.BlobConfig)
	if err != nil {
		return configError{err}
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
		"provider", cfg.Storage.GetProvider(),
	)
	fmt.Fprintf(os.Stdout, "uploaded %s (%d bytes, %d files)\n", key, len(blob), len(matches))
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
