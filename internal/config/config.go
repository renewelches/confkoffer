// Package config loads and resolves the .confkoffer.yaml schema.
//
// Resolution order for any field that can be overridden:
//
//	CLI flag (Override.Set == true) > env var > YAML > built-in default
//
// The package returns ErrMissingRequired (sentinel) for missing
// required fields so cmd/ can map it to exit code 2.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"confkoffer/internal/crypto"
)

// DefaultBucket is used when neither YAML nor flag/env supply one.
const DefaultBucket = "confkoffer"

// DefaultRegion is the AWS-compatible fallback region.
const DefaultRegion = "us-east-1"

// DefaultConfigPath is the YAML file we look for in CWD when --config
// isn't passed.
const DefaultConfigPath = ".confkoffer.yaml"

// Env var names. Centralised so cmd/ and tests refer to one source of truth.
const (
	EnvName     = "CONFKOFFER_NAME"
	EnvBucket   = "CONFKOFFER_BUCKET"
	EnvEndpoint = "AWS_ENDPOINT"
	EnvRegion   = "AWS_REGION"
	EnvPassword = "CONFKOFFER_PASS"
)

// ErrMissingRequired wraps the message reported when one of name /
// endpoint is unset after the full resolution chain.
var ErrMissingRequired = errors.New("missing required configuration")

// Config is the resolved, ready-to-use configuration object passed
// down into pack/unpack/list. Plain data — no behaviour.
type Config struct {
	Name     string
	Storage  Storage
	Crypto   Crypto
	Patterns Patterns
	Password Password

	// Password (if any) explicitly supplied via CLI flag or env. Held
	// here so the password Source chain can read it without re-parsing
	// flags. Wiped after use by callers.
	PasswordOverride []byte

	// Source path for the YAML, for debug logging and error messages.
	SourcePath string
}

// Storage holds bucket + endpoint coordinates.
type Storage struct {
	Bucket   string
	Endpoint string
	Region   string
}

// Crypto wraps argon2id KDF params.
type Crypto struct {
	Argon2id crypto.Params
}

// Patterns is the explicit include/exclude set for the scanner.
// Mirrors scan.Patterns to avoid an import cycle through cmd/.
type Patterns struct {
	Include []string
	Exclude []string
}

// Password is the resolved password subsystem configuration.
//
// Source determines which Source implementation cmd/ wires up. When
// empty, cmd/ uses the default chain (flag -> env -> prompt).
type Password struct {
	Source  string
	Pass    PassConfig
	Command CommandConfig
}

// PassConfig configures the passwordstore.org integration.
type PassConfig struct {
	Path string
}

// CommandConfig configures the universal exec source.
type CommandConfig struct {
	Argv    []string
	Timeout time.Duration
}

// Override is the wire format for a single CLI flag value: the value
// itself plus a "was the flag explicitly set" bit so the resolver can
// distinguish "user typed --region" from "default still applies".
type Override struct {
	Value string
	Set   bool
}

// Overrides bundles all CLI overrides cmd/ wants to layer on top of the
// YAML config. Add fields here as new flags are introduced.
type Overrides struct {
	Name      Override
	Bucket    Override
	Endpoint  Override
	Region    Override
	Password  Override
	ConfigDir string // defaults to "" — overrides handled by Load's path arg
}

// rawConfig is the YAML wire schema. We decode strictly so unknown keys
// surface as schema errors instead of silent misconfigurations.
type rawConfig struct {
	Name     string         `yaml:"name"`
	Storage  rawStorage     `yaml:"storage"`
	Crypto   rawCrypto      `yaml:"crypto"`
	Patterns rawPatterns    `yaml:"patterns"`
	Password rawPasswordCfg `yaml:"password"`
}

type rawStorage struct {
	Bucket   string `yaml:"bucket"`
	Endpoint string `yaml:"endpoint"`
	Region   string `yaml:"region"`
}

type rawCrypto struct {
	Argon2id rawArgon2id `yaml:"argon2id"`
}

type rawArgon2id struct {
	MemoryKiB uint32 `yaml:"memory_kib"`
	Time      uint8  `yaml:"time"`
	Threads   uint8  `yaml:"threads"`
}

type rawPatterns struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

type rawPasswordCfg struct {
	Source  string         `yaml:"source"`
	Pass    rawPass        `yaml:"pass"`
	Command rawPassCommand `yaml:"command"`
}

type rawPass struct {
	Path string `yaml:"path"`
}

type rawPassCommand struct {
	Argv    []string `yaml:"argv"`
	Timeout string   `yaml:"timeout"`
}

// Load reads the YAML at path and returns a Config with YAML values
// applied (no flag/env layering yet — call Resolve for that). If path
// is empty, the file is optional and an empty Config is returned. If
// the file is present, decoding is strict (unknown keys are errors).
func Load(path string) (*Config, error) {
	cfg := &Config{
		SourcePath: path,
		Storage:    Storage{},
	}
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Treat absence the same as "no config file" — cmd/ may
			// still satisfy required fields via flags/env.
			cfg.SourcePath = ""
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)

	var raw rawConfig
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	cfg.Name = raw.Name
	cfg.Storage.Bucket = raw.Storage.Bucket
	cfg.Storage.Endpoint = raw.Storage.Endpoint
	cfg.Storage.Region = raw.Storage.Region

	if raw.Crypto.Argon2id.MemoryKiB != 0 || raw.Crypto.Argon2id.Time != 0 || raw.Crypto.Argon2id.Threads != 0 {
		cfg.Crypto.Argon2id = crypto.Params{
			MemoryKiB: raw.Crypto.Argon2id.MemoryKiB,
			Time:      raw.Crypto.Argon2id.Time,
			Threads:   raw.Crypto.Argon2id.Threads,
		}
	}

	cfg.Patterns = Patterns{
		Include: raw.Patterns.Include,
		Exclude: raw.Patterns.Exclude,
	}

	cfg.Password = Password{
		Source:  raw.Password.Source,
		Pass:    PassConfig{Path: raw.Password.Pass.Path},
		Command: CommandConfig{Argv: raw.Password.Command.Argv},
	}
	if raw.Password.Command.Timeout != "" {
		d, err := time.ParseDuration(raw.Password.Command.Timeout)
		if err != nil {
			return nil, fmt.Errorf("password.command.timeout: %w", err)
		}
		cfg.Password.Command.Timeout = d
	}

	return cfg, nil
}

// Resolve layers env vars and CLI overrides on top of the YAML-loaded
// Config and applies built-in defaults. It then validates that all
// required fields are present and well-formed.
//
// The caller passes Overrides built from its pflag.FlagSet (Set is true
// iff Flag.Changed returned true). Env lookups use os.Getenv.
func Resolve(cfg *Config, ov Overrides) error {
	if cfg == nil {
		return errors.New("nil config")
	}

	cfg.Name = pickString(cfg.Name, ov.Name, EnvName, "")
	cfg.Storage.Bucket = pickString(cfg.Storage.Bucket, ov.Bucket, EnvBucket, DefaultBucket)
	cfg.Storage.Endpoint = pickString(cfg.Storage.Endpoint, ov.Endpoint, EnvEndpoint, "")
	cfg.Storage.Region = pickString(cfg.Storage.Region, ov.Region, EnvRegion, DefaultRegion)

	// Apply Argon2id defaults only if YAML didn't specify any. We treat
	// "all three zero" as "unset" — the validator catches partial fills.
	if cfg.Crypto.Argon2id == (crypto.Params{}) {
		cfg.Crypto.Argon2id = crypto.DefaultParams()
	}

	if ov.Password.Set {
		cfg.PasswordOverride = []byte(ov.Password.Value)
	}

	// Required fields.
	var missing []string
	if cfg.Name == "" {
		missing = append(missing, "name")
	}
	if cfg.Storage.Endpoint == "" {
		missing = append(missing, "storage.endpoint")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMissingRequired, strings.Join(missing, ", "))
	}

	// Validation.
	if err := ValidateName(cfg.Name); err != nil {
		return err
	}
	if err := cfg.Crypto.Argon2id.Validate(); err != nil {
		return fmt.Errorf("crypto.argon2id: %w", err)
	}
	if cfg.Password.Source != "" {
		if err := validatePasswordSource(cfg.Password); err != nil {
			return err
		}
	}
	return nil
}

// pickString implements: override.Set wins, then env, then existing
// (YAML), then default.
func pickString(yamlVal string, ov Override, envKey, def string) string {
	if ov.Set {
		return ov.Value
	}
	if envKey != "" {
		if v, ok := os.LookupEnv(envKey); ok && v != "" {
			return v
		}
	}
	if yamlVal != "" {
		return yamlVal
	}
	return def
}
