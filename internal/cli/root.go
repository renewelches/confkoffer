// Package cli wires the cobra command tree.
//
// Exit-code mapping:
//   - 0: success
//   - 1: general runtime error (network, decrypt, no snapshots, IO)
//   - 2: config error (missing required, bad YAML, bad name, prompt
//     retries exhausted)
//
// Sub-commands return a regular error and Execute classifies it.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/renewelches/confkoffer/internal/config"
	"github.com/renewelches/confkoffer/internal/logging"
	"github.com/renewelches/confkoffer/internal/password"
)

// rootFlags is the canonical home for the global flag values; subcmds
// read from it after PersistentPreRunE has populated it.
type rootFlags struct {
	LogLevel string
	Region   string
	Config   string
}

var (
	root     = newRoot()
	rootOpts rootFlags
)

func newRoot() *cobra.Command {
	c := &cobra.Command{
		Use:   "confkoffer",
		Short: "Pack, encrypt, and ship configuration files to an S3-compatible bucket.",
		Long: `confkoffer bundles, encrypts, and uploads project configuration to an
S3-compatible bucket — and reverses the flow on retrieval. Use it to
keep sensitive files (provider credentials, env files, backend
configs) safely backed up off-machine.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	c.PersistentFlags().StringVar(&rootOpts.LogLevel, "log-level", "info", "log level: debug|info|warn|error")
	c.PersistentFlags().StringVar(&rootOpts.Region, "region", "", "S3 region (overrides config; default us-east-1)")
	c.PersistentFlags().StringVar(&rootOpts.Config, "config", config.DefaultConfigPath, "path to .confkoffer.yaml (CWD-only by default)")

	c.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		level, err := logging.ParseLevel(rootOpts.LogLevel)
		if err != nil {
			return configError{err}
		}
		logging.Init(os.Stderr, level)
		return nil
	}

	return c
}

// Execute runs the cobra root and translates the resulting error into
// an exit code. Called from main.
func Execute() {
	addPack(root)
	addUnpack(root)
	addList(root)
	addInit(root)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	err := root.ExecuteContext(ctx)
	if err == nil {
		return
	}
	code := exitCodeFor(err)
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(code)
}

// configError wraps any error that should map to exit code 2.
type configError struct{ err error }

func (c configError) Error() string { return c.err.Error() }
func (c configError) Unwrap() error { return c.err }

// exitCodeFor classifies err into one of the documented exit codes.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var ce configError
	if errors.As(err, &ce) {
		return 2
	}
	if errors.Is(err, config.ErrMissingRequired) {
		return 2
	}
	if errors.Is(err, password.ErrPromptExhausted) {
		return 2
	}
	return 1
}

// loadAndResolveConfig is the shared helper used by pack/unpack/list:
// load YAML (optional), apply CLI/env overrides from flagSet, validate.
//
// flagOverride helpers fetch the per-subcommand flag values that
// participate in resolution (--name, --bucket, --endpoint, --pass).
func loadAndResolveConfig(cmd *cobra.Command, ov config.Overrides) (*config.Config, error) {
	// Region is global — fold it into Overrides.
	if cmd.Root().PersistentFlags().Changed("region") {
		ov.Region = config.Override{Value: rootOpts.Region, Set: true}
	}

	cfg, err := config.Load(rootOpts.Config)
	if err != nil {
		return nil, configError{err}
	}
	if err := config.Resolve(cfg, ov); err != nil {
		return nil, configError{err}
	}
	slog.Debug("config resolved",
		"name", cfg.Name,
		"bucket", cfg.Storage.Bucket,
		"endpoint", cfg.Storage.Endpoint,
		"region", cfg.Storage.Region,
	)
	return cfg, nil
}

// stringFlag pulls a string flag and reports whether it was set.
func stringFlag(cmd *cobra.Command, name string) config.Override {
	v, _ := cmd.Flags().GetString(name)
	return config.Override{Value: v, Set: cmd.Flags().Changed(name)}
}
