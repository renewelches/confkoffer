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
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/renewelches/confkoffer/internal/crypto"
	"go.yaml.in/yaml/v3"
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

// ErrMissingRequired wraps the message reported when a required field is
// unset after the full resolution chain.
var ErrMissingRequired = errors.New("missing required configuration")

// Config is the resolved, ready-to-use configuration object passed
// down into pack/unpack/list. Plain data — no behaviour.
//
// Fields that are not part of the YAML schema carry `yaml:"-"`. Without
// it they would be settable from the config file: KnownFields(true)
// only rejects keys with no matching field, and these fields do exist.
type Config struct {
	Name     string       `yaml:"name"`
	Storage  StorageField `yaml:"storage"`
	Crypto   Crypto       `yaml:"crypto"`
	Patterns Patterns     `yaml:"patterns"`
	Password Password     `yaml:"password"`

	// Password (if any) explicitly supplied via CLI flag or env. Held
	// here so the password Source chain can read it without re-parsing
	// flags. Wiped after use by callers.
	PasswordOverride []byte `yaml:"-"`

	// Source path for the YAML, for debug logging and error messages.
	SourcePath string `yaml:"-"`
}

// BlobConfig is the provider-specific storage configuration. Both
// methods are exported because internal/store consumes them —
// unexported interface methods are callable only within this package.
type BlobConfig interface {
	GetProvider() string
	Validate() error
}

// ProviderConfig is embedded in every provider config and supplies the
// GetProvider half of BlobConfig by method promotion.
type ProviderConfig struct {
	Provider string `yaml:"provider"`
}

func (pc *ProviderConfig) GetProvider() string { return pc.Provider }

// S3Config configures any S3-compatible object store.
//
// An empty Endpoint means AWS S3 itself. Set Endpoint for MinIO, Ceph
// RadosGW, StackIT, Wasabi, R2, Spaces and the rest — or for an AWS
// FIPS, dualstack, or VPC interface endpoint, which are legitimate
// overrides against AWS proper.
//
// Reached through the provider aliases "aws", "s3", and "minio". They
// select the same struct and differ only in whether Endpoint is
// required; see Validate.
type S3Config struct {
	ProviderConfig `yaml:",inline"`
	Bucket         string `yaml:"bucket"`
	Region         string `yaml:"region"`
	Endpoint       string `yaml:"endpoint"`

	// Insecure forces http:// instead of https://. Useful for local
	// MinIO. Defaults to false so a missing key never silently
	// downgrades transport.
	Insecure bool `yaml:"insecure"`
}

func (c *S3Config) Validate() error {
	fields := map[string]string{"bucket": c.Bucket}
	// "aws" names AWS S3, where Endpoint is an optional override. Any
	// other alias is an explicit statement that this is *not* AWS, so a
	// missing endpoint would silently redirect uploads to AWS — an
	// unpleasant way to discover a typo. Require it there.
	if c.Provider != "aws" {
		fields["endpoint"] = c.Endpoint
	}
	return requireFields(c.Provider, fields)
}

// AzureConfig addresses a container in Azure Blob Storage.
//
// Only the container is named here, for the same reasons GCPConfig
// names only a bucket:
//
//   - The storage account comes from the environment
//     (AZURE_STORAGE_ACCOUNT, beside AZURE_STORAGE_KEY /
//     AZURE_STORAGE_CONNECTION_STRING / AZURE_STORAGE_SAS_TOKEN) — the
//     same place the credentials live, and inseparable from them.
//     Repeating it in config would only let the two disagree.
//
//   - The region is a property of the storage account, chosen when the
//     account is created and encoded in its DNS name
//     (<account>.blob.core.windows.net). It is not a client setting, so
//     a location field here would be inert. Data residency is decided
//     when the account is created.
type AzureConfig struct {
	ProviderConfig `yaml:",inline"`
	ContainerID    string `yaml:"containerid"`
}

func (ac *AzureConfig) Validate() error {
	return requireFields("azure", map[string]string{"containerid": ac.ContainerID})
}

// FileConfig points at a plain directory — a local disk path or any
// mounted share. confkoffer does not care which; the mount is the
// operator's concern.
type FileConfig struct {
	ProviderConfig `yaml:",inline"`
	DirPath        string `yaml:"dirpath"`
}

func (fc *FileConfig) Validate() error {
	return requireFields("file", map[string]string{"dirpath": fc.DirPath})
}

// GCPConfig addresses a Google Cloud Storage bucket.
//
// There is deliberately no project and no region field:
//
//   - The project comes from Application Default Credentials, the same
//     place the credentials themselves come from. Naming it again in
//     config would only create a way for the two to disagree.
//
//   - A bucket's location (US, EU, europe-west3, me-central2, …) is
//     fixed when the bucket is created and is immutable thereafter.
//     "gs://<bucket>" resolves globally, so a client never names a
//     region. This differs from S3, whose SigV4 signature embeds the
//     region; GCS authenticates with OAuth bearer tokens that carry no
//     location. A region field here would be inert.
//
// Data residency is therefore a property of how the bucket was created
// (gcloud storage buckets create --location=me-central2), not something
// confkoffer can or should configure.
type GCPConfig struct {
	ProviderConfig `yaml:",inline"`
	Bucket         string `yaml:"bucket"`

	// UniverseDomain targets a non-public Google universe — sovereign or
	// partner clouds that serve a different API domain than
	// googleapis.com. Leave empty for public GCP. This is the only
	// client-side control related to residency.
	UniverseDomain string `yaml:"universe_domain"`
}

func (gc *GCPConfig) Validate() error {
	return requireFields("gcp", map[string]string{"bucket": gc.Bucket})
}

// requireFields reports the empty entries of fields as a single
// ErrMissingRequired-wrapped error, listing them in stable order.
func requireFields(provider string, fields map[string]string) error {
	var missing []string
	for k, v := range fields {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%w: storage provider %s requires %s",
		ErrMissingRequired, provider, strings.Join(missing, ", "))
}

// StorageField adapts the polymorphic storage block to yaml decoding.
// The concrete type is not known until the "provider" key has been
// read, so the node is probed first and decoded second — see
// CreateStorageConfig.
//
// The embedded interface promotes GetProvider/Validate, so callers can
// write cfg.Storage.GetProvider() directly.
type StorageField struct {
	BlobConfig
}

// UnmarshalYAML implements yaml.Unmarshaler. Declaring it here is what
// removes the need for a separate wire-format struct: the decoder calls
// back into this method when it reaches the storage node, so the rest
// of Config decodes straight into its domain types.
func (s *StorageField) UnmarshalYAML(n *yaml.Node) error {
	bc, err := CreateStorageConfig(n)
	if err != nil {
		return err
	}
	s.BlobConfig = bc
	return nil
}

// CreateStorageConfig builds the provider-specific BlobConfig described
// by the storage node.
//
// The "provider" key selects the backend and determines which further
// keys apply:
//
//	aws            bucket  (optional: region, endpoint, insecure)
//	s3 | minio     bucket, endpoint  (optional: region, insecure)
//	azure          containerid
//	file           dirpath
//	gcp            bucket  (optional: universe_domain)
//
// The three S3 aliases select the same S3Config. "aws" targets AWS S3
// and treats endpoint as an optional override; "s3" and "minio" say
// explicitly that this is not AWS and so require one.
//
// Keys not belonging to the selected provider are rejected. The
// decoder's own KnownFields(true) cannot do this: it never sees inside
// the storage node, because that node is decoded separately here.
//
// Otherwise only shape errors are reported — an unreadable node or an
// unrecognised provider. Missing required keys are the business of
// BlobConfig.Validate, which runs after env/flag overrides have been
// layered on and so is the only point at which "missing" is knowable.
func CreateStorageConfig(n *yaml.Node) (BlobConfig, error) {
	if n == nil {
		return nil, errors.New("storage: empty configuration")
	}
	var probe ProviderConfig
	if err := n.Decode(&probe); err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}

	var bCnfg BlobConfig
	switch probe.Provider {
	case "":
		return nil, fmt.Errorf("line %d: storage is missing the provider key", n.Line)
	case "aws", "s3", "minio":
		bCnfg = &S3Config{}
	case "azure":
		bCnfg = &AzureConfig{}
	case "file":
		bCnfg = &FileConfig{}
	case "gcp":
		bCnfg = &GCPConfig{}
	default:
		return nil, fmt.Errorf("line %d: storage has invalid provider %q; allowed values are: aws, azure, file, gcp, minio, s3",
			n.Line, probe.Provider)
	}
	if err := checkStorageKeys(n, bCnfg, probe.Provider); err != nil {
		return nil, err
	}
	if err := n.Decode(bCnfg); err != nil {
		return nil, fmt.Errorf("storage (%s): %w", probe.Provider, err)
	}
	return bCnfg, nil
}

// checkStorageKeys rejects any key in n that the provider config does
// not declare, restoring for the storage block the strictness that
// KnownFields(true) gives the rest of the schema.
func checkStorageKeys(n *yaml.Node, bCnfg BlobConfig, provider string) error {
	return checkKnownKeys(n, reflect.TypeOf(bCnfg), "storage provider "+provider)
}

// checkKnownKeys rejects any key in the mapping n that type t does not
// declare, naming the offender and listing what was allowed.
//
// Every type in this package with its own UnmarshalYAML needs this.
// The decoder's KnownFields(true) applies only to nodes it decodes
// itself; the moment a custom unmarshaler takes over, the subtree is
// decoded through yaml.Node.Decode, which carries no such setting. So
// strictness is not inherited — it has to be re-established at each
// custom unmarshaler, or a typo in that subtree is silently discarded.
//
// what names the block for the error message ("crypto.argon2id",
// "storage provider minio").
func checkKnownKeys(n *yaml.Node, t reflect.Type, what string) error {
	// Non-mappings never get here: the caller's Decode already rejects
	// them, with a better message than we could give. A mapping's
	// Content is [key, value, key, value, ...]; anything without that
	// shape simply yields no iterations below.
	allowed := yamlFieldNames(t)
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i]
		if allowed[key.Value] {
			continue
		}
		names := make([]string, 0, len(allowed))
		for k := range allowed {
			names = append(names, k)
		}
		sort.Strings(names)
		return fmt.Errorf("line %d: %s has unknown key %q; allowed keys are: %s",
			key.Line, what, key.Value, strings.Join(names, ", "))
	}
	return nil
}

// childNode returns the value node for key in the mapping n, or nil.
func childNode(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// yamlFieldNames collects the yaml key names t accepts, following
// inline-embedded structs. Deriving the set from the struct itself
// keeps it from drifting out of sync with the type.
func yamlFieldNames(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return
		}
		for f := range t.Fields() {
			name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if strings.Contains(opts, "inline") || (f.Anonymous && name == "") {
				walk(f.Type)
				continue
			}
			if name == "-" {
				continue
			}
			if name == "" {
				name = strings.ToLower(f.Name)
			}
			out[name] = true
		}
	}
	walk(t)
	return out
}

// Crypto wraps argon2id KDF params.
type Crypto struct {
	Argon2id crypto.Params `yaml:"argon2id"`
}

// argon2idYAML is the wire shape for crypto.Params. It exists because
// crypto.Params lives in another package and so cannot carry the
// yaml tags that map memory_kib onto MemoryKiB.
type argon2idYAML struct {
	MemoryKiB uint32 `yaml:"memory_kib"`
	Time      uint8  `yaml:"time"`
	Threads   uint8  `yaml:"threads"`
}

// cryptoYAML is the wire shape for the crypto block. Named rather than
// anonymous so checkKnownKeys can derive its allowed keys by reflection.
type cryptoYAML struct {
	Argon2id argon2idYAML `yaml:"argon2id"`
}

// UnmarshalYAML implements yaml.Unmarshaler for the snake_case mapping.
//
// Both levels are key-checked. Silently dropping a typo here is not a
// cosmetic failure: an unrecognised `memory_kb` leaves Params zero,
// Resolve reads all-zero as "unset", and the operator's deliberately
// hardened KDF parameters are replaced by the defaults with nothing
// printed. The snapshot still encrypts, just not the way they asked.
func (c *Crypto) UnmarshalYAML(n *yaml.Node) error {
	if err := checkKnownKeys(n, reflect.TypeOf(cryptoYAML{}), "crypto"); err != nil {
		return err
	}
	if argon := childNode(n, "argon2id"); argon != nil {
		if err := checkKnownKeys(argon, reflect.TypeOf(argon2idYAML{}), "crypto.argon2id"); err != nil {
			return err
		}
	}
	var raw cryptoYAML
	if err := n.Decode(&raw); err != nil {
		return err
	}
	c.Argon2id = crypto.Params{
		MemoryKiB: raw.Argon2id.MemoryKiB,
		Time:      raw.Argon2id.Time,
		Threads:   raw.Argon2id.Threads,
	}
	return nil
}

// Patterns is the explicit include/exclude set for the scanner.
// Mirrors scan.Patterns to avoid an import cycle through cmd/.
type Patterns struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// Password is the resolved password subsystem configuration.
//
// Source determines which Source implementation cmd/ wires up. When
// empty, cmd/ uses the default chain (flag -> env -> prompt).
type Password struct {
	Source  string        `yaml:"source"`
	Pass    PassConfig    `yaml:"pass"`
	Command CommandConfig `yaml:"command"`
}

// PassConfig configures the passwordstore.org integration.
type PassConfig struct {
	Path string `yaml:"path"`
}

// CommandConfig configures the universal exec source. Timeout decodes
// directly from a duration string such as "10s" — yaml.v3 understands
// time.Duration natively, so no string-then-parse step is needed.
type CommandConfig struct {
	Argv    []string      `yaml:"argv"`
	Timeout time.Duration `yaml:"timeout"`
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

// Load reads the YAML at path and returns a Config with YAML values
// applied (no flag/env layering yet — call Resolve for that). If path
// is empty, the file is optional and an empty Config is returned. If
// the file is present, decoding is strict (unknown keys are errors).
func Load(path string) (*Config, error) {
	cfg := &Config{SourcePath: path}
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
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.SourcePath = path
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
	if cfg.Storage.BlobConfig == nil {
		cfg.Storage.BlobConfig = inferS3Storage(ov)
	}
	if cfg.Storage.BlobConfig != nil {
		if err := applyStorageOverrides(cfg.Storage.BlobConfig, ov); err != nil {
			return err
		}
	}

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
	if cfg.Storage.BlobConfig == nil {
		missing = append(missing, "storage")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMissingRequired, strings.Join(missing, ", "))
	}

	// Validation.
	if err := ValidateName(cfg.Name); err != nil {
		return err
	}
	if err := cfg.Storage.Validate(); err != nil {
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

// inferS3Storage synthesises an S3 storage config when the YAML supplied
// none but the caller named a bucket or endpoint explicitly. It returns
// nil otherwise, leaving Resolve to report storage as missing.
//
// This keeps confkoffer usable with no config file at all:
//
//	confkoffer unpack --name proj --bucket b --endpoint https://minio.example:9000
//
// which matters because .confkoffer.yaml is CWD-only and, on the fresh
// machine where you most need a restore, is often the very file you are
// trying to recover.
//
// The provider is "aws" because that reproduces the pre-provider schema,
// where storage was implicitly S3. "aws" treats endpoint as an optional
// override, so this works against self-hosted S3 too.
//
// Only an explicit bucket or endpoint counts. Bucket has a built-in
// default, so triggering on the resolved value would make every bare
// invocation synthesise a config pointing at a bucket the user never
// named — trading a clear "storage is missing" for a confusing failure
// at connection time.
func inferS3Storage(ov Overrides) BlobConfig {
	if overrideValue(ov.Bucket, EnvBucket) == "" && overrideValue(ov.Endpoint, EnvEndpoint) == "" {
		return nil
	}
	// Field values are filled in by applyStorageOverrides, which runs next.
	return &S3Config{ProviderConfig: ProviderConfig{Provider: "aws"}}
}

// overrideValue reports the flag or env value for ov, ignoring YAML and
// built-in defaults. Unlike pickString it answers "did the caller say
// this?" rather than "what is the effective value?".
func overrideValue(ov Override, envKey string) string {
	if ov.Set {
		return ov.Value
	}
	if envKey != "" {
		if v, ok := os.LookupEnv(envKey); ok {
			return v
		}
	}
	return ""
}

// applyStorageOverrides layers flag/env values onto the provider config.
//
// --bucket, --endpoint, and --region describe an S3 endpoint and have
// no meaning for the other providers: azure is addressed by container,
// gcp by bucket name alone, file by directory. Passing one anyway is
// reported rather than dropped — a flag that appears to work but
// changes nothing is how a snapshot ends up somewhere the operator did
// not intend.
//
// Only explicit flags are rejected. The env vars are ambient and may
// well be exported for some other tool in the same shell, so treating
// their mere presence as an error would break unrelated workflows.
func applyStorageOverrides(bc BlobConfig, ov Overrides) error {
	if c, ok := bc.(*S3Config); ok {
		c.Bucket = pickString(c.Bucket, ov.Bucket, EnvBucket, DefaultBucket)
		c.Region = pickString(c.Region, ov.Region, EnvRegion, DefaultRegion)
		c.Endpoint = pickString(c.Endpoint, ov.Endpoint, EnvEndpoint, "")
		return nil
	}

	var unsupported []string
	for _, f := range []struct {
		flag string
		ov   Override
	}{
		{"--bucket", ov.Bucket},
		{"--endpoint", ov.Endpoint},
		{"--region", ov.Region},
	} {
		if f.ov.Set {
			unsupported = append(unsupported, f.flag)
		}
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("storage provider %s does not accept %s; %s",
			bc.GetProvider(), strings.Join(unsupported, ", "),
			"those flags configure an S3 endpoint (providers: aws, s3, minio)")
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
