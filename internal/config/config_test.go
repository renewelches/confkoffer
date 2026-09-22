package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// validStorage returns a storage block that satisfies Validate, for
// tests whose subject is something other than storage itself.
func validStorage() StorageField {
	return StorageField{BlobConfig: &S3Config{
		ProviderConfig: ProviderConfig{Provider: "minio"},
		Bucket:         "yaml-bucket",
		Endpoint:       "yaml-ep",
	}}
}

// s3Of asserts the resolved storage is an *S3Config and returns it.
func s3Of(t *testing.T, cfg *Config) *S3Config {
	t.Helper()
	c, ok := cfg.Storage.BlobConfig.(*S3Config)
	if !ok {
		t.Fatalf("Storage is %T, want *S3Config", cfg.Storage.BlobConfig)
	}
	return c
}

func writeYAML(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".confkoffer.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFullSchema(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
name: my-project
storage:
  provider: minio
  bucket: my-backups
  endpoint: s3.example.com
  region: eu-central-1
crypto:
  argon2id:
    memory_kib: 47104
    time: 1
    threads: 1
patterns:
  include:
    - "**/*.tf"
    - "secrets/prod.env"
  exclude:
    - "**/*.tfstate"
password:
  source: pass
  pass:
    path: backups/confkoffer/my-project
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "my-project" {
		t.Errorf("Name=%q", cfg.Name)
	}
	st := s3Of(t, cfg)
	if st.Bucket != "my-backups" || st.Endpoint != "s3.example.com" || st.Region != "eu-central-1" {
		t.Errorf("Storage=%+v", st)
	}
	if cfg.Crypto.Argon2id.MemoryKiB != 47104 || cfg.Crypto.Argon2id.Time != 1 {
		t.Errorf("Argon2id=%+v", cfg.Crypto.Argon2id)
	}
	want := []string{"**/*.tf", "secrets/prod.env"}
	if slices.Compare(cfg.Patterns.Include, want) != 0 {
		t.Errorf("Include=%v want %v", cfg.Patterns.Include, want)
	}
	if cfg.Password.Source != "pass" || cfg.Password.Pass.Path == "" {
		t.Errorf("Password=%+v", cfg.Password)
	}
}

func TestLoadMissingFileIsOptional(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg.Name != "" {
		t.Errorf("expected zero-value Name, got %q", cfg.Name)
	}
}

func TestLoadEmptyPathIsOptional(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("nil cfg")
	}
}

func TestLoadStrictSchemaRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
name: foo
storage:
  provider: minio
  bukket: typo
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error on unknown key 'bukket'")
	}
	if !strings.Contains(err.Error(), "bukket") {
		t.Errorf("error = %q, want it to name the offending key", err)
	}
}

func TestLoadStrictSchemaRejectsUnknownTopLevelKey(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
name: foo
bukket: typo
storage:
  provider: minio
  bucket: b
  endpoint: e
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error on unknown top-level key 'bukket'")
	}
}

// Crypto has its own UnmarshalYAML, and a custom unmarshaler decodes
// through yaml.Node.Decode, which does not inherit KnownFields(true).
// Without an explicit check the block silently accepts anything.
//
// The consequence is not cosmetic. An unrecognised `memory_kb` leaves
// Params zero, Resolve reads all-zero as "unset" and substitutes the
// defaults, and the operator's hardened KDF settings are gone with
// nothing printed to say so.
func TestLoadStrictSchemaRejectsUnknownCryptoKey(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		badKey string
	}{
		{
			name: "typo in an argon2id parameter",
			yaml: `
name: foo
crypto:
  argon2id:
    memory_kb: 47104
`,
			badKey: "memory_kb",
		},
		{
			name: "typo in the argon2id key itself",
			yaml: `
name: foo
crypto:
  argon2idd:
    memory_kib: 47104
`,
			badKey: "argon2idd",
		},
		{
			name: "extra parameter",
			yaml: `
name: foo
crypto:
  argon2id:
    memory_kib: 47104
    time: 1
    threads: 1
    parallelism: 4
`,
			badKey: "parallelism",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := writeYAML(t, t.TempDir(), tc.yaml)
			_, err := Load(p)
			if err == nil {
				t.Fatalf("Load() = nil error, want it to reject %q", tc.badKey)
			}
			if !strings.Contains(err.Error(), tc.badKey) {
				t.Errorf("error = %q, want it to name %q", err, tc.badKey)
			}
		})
	}
}

// The allowlist is derived from the struct tags, so every declared key
// must still be accepted.
func TestLoadAcceptsEveryDeclaredCryptoKey(t *testing.T) {
	p := writeYAML(t, t.TempDir(), `
name: foo
crypto:
  argon2id:
    memory_kib: 47104
    time: 1
    threads: 1
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Crypto.Argon2id.MemoryKiB != 47104 {
		t.Errorf("MemoryKiB = %d, want 47104", cfg.Crypto.Argon2id.MemoryKiB)
	}
}

func TestResolveAppliesDefaults(t *testing.T) {
	t.Setenv(EnvName, "")
	t.Setenv(EnvBucket, "")
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvRegion, "")

	cfg, _ := Load("")
	cfg.Name = "proj"
	cfg.Storage = StorageField{BlobConfig: &S3Config{
		ProviderConfig: ProviderConfig{Provider: "minio"},
		Endpoint:       "s3.example.com",
	}}

	if err := Resolve(cfg, Overrides{}); err != nil {
		t.Fatal(err)
	}
	st := s3Of(t, cfg)
	if st.Bucket != DefaultBucket {
		t.Errorf("Bucket=%q want %q", st.Bucket, DefaultBucket)
	}
	if st.Region != DefaultRegion {
		t.Errorf("Region=%q want %q", st.Region, DefaultRegion)
	}
}

func TestResolvePrecedenceFlagBeatsEnvBeatsYAMLBeatsDefault(t *testing.T) {
	// flag wins
	cfg := &Config{Name: "yaml", Storage: validStorage()}
	t.Setenv(EnvBucket, "env-bucket")
	t.Setenv(EnvName, "env-name")
	t.Setenv(EnvEndpoint, "env-ep")

	err := Resolve(cfg, Overrides{
		Name:   Override{Value: "flag-name", Set: true},
		Bucket: Override{Value: "flag-bucket", Set: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "flag-name" {
		t.Errorf("Name=%q, want flag-name", cfg.Name)
	}
	st := s3Of(t, cfg)
	if st.Bucket != "flag-bucket" {
		t.Errorf("Bucket=%q, want flag-bucket", st.Bucket)
	}
	// env wins for endpoint (no flag override)
	if st.Endpoint != "env-ep" {
		t.Errorf("Endpoint=%q, want env-ep", st.Endpoint)
	}
}

func TestResolveEnvBeatsYAML(t *testing.T) {
	cfg := &Config{Name: "yaml", Storage: validStorage()}
	t.Setenv(EnvBucket, "env-bucket")

	if err := Resolve(cfg, Overrides{}); err != nil {
		t.Fatal(err)
	}
	if st := s3Of(t, cfg); st.Bucket != "env-bucket" {
		t.Errorf("Bucket=%q, want env-bucket", st.Bucket)
	}
}

func TestResolveYAMLBeatsDefault(t *testing.T) {
	// no env layer, so YAML is the highest-priority source present
	t.Setenv(EnvBucket, "")
	t.Setenv(EnvEndpoint, "")
	cfg := &Config{Name: "yaml", Storage: validStorage()}
	if err := Resolve(cfg, Overrides{}); err != nil {
		t.Fatal(err)
	}
	if st := s3Of(t, cfg); st.Bucket != "yaml-bucket" {
		t.Errorf("Bucket=%q, want yaml-bucket", st.Bucket)
	}
}

func TestResolveMissingNameIsExitTwo(t *testing.T) {
	t.Setenv(EnvName, "")
	cfg, _ := Load("")
	cfg.Storage = validStorage()
	err := Resolve(cfg, Overrides{})
	if !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("got %v, want ErrMissingRequired", err)
	}
}

func TestResolveMissingEndpointIsExitTwo(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	cfg, _ := Load("")
	cfg.Name = "proj"
	// storage is present and well-formed apart from the endpoint, so
	// this exercises S3Config.Validate rather than the nil-storage path.
	cfg.Storage = StorageField{BlobConfig: &S3Config{
		ProviderConfig: ProviderConfig{Provider: "minio"},
		Bucket:         "b",
	}}
	err := Resolve(cfg, Overrides{})
	if !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("got %v, want ErrMissingRequired", err)
	}
	if !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("error = %q, want it to name the endpoint", err)
	}
}

// With no config file, an explicit bucket or endpoint synthesises an S3
// storage config. This keeps a restore possible on a fresh machine,
// where .confkoffer.yaml is often the very file being recovered.
func TestResolveInfersS3StorageFromFlags(t *testing.T) {
	t.Setenv(EnvBucket, "")
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvRegion, "")

	cfg, _ := Load("") // no config file
	err := Resolve(cfg, Overrides{
		Name:     Override{Value: "my-project", Set: true},
		Bucket:   Override{Value: "confkoffer", Set: true},
		Endpoint: Override{Value: "https://minio.example:9000", Set: true},
	})
	if err != nil {
		t.Fatalf("Resolve() = %v, want nil", err)
	}
	st := s3Of(t, cfg)
	if st.Provider != "aws" {
		t.Errorf("Provider=%q, want aws", st.Provider)
	}
	if st.Bucket != "confkoffer" {
		t.Errorf("Bucket=%q", st.Bucket)
	}
	if st.Endpoint != "https://minio.example:9000" {
		t.Errorf("Endpoint=%q", st.Endpoint)
	}
	if st.Region != DefaultRegion {
		t.Errorf("Region=%q, want %q", st.Region, DefaultRegion)
	}
}

func TestResolveInfersS3StorageFromEnv(t *testing.T) {
	t.Setenv(EnvBucket, "env-bucket")
	t.Setenv(EnvEndpoint, "env-endpoint")

	cfg, _ := Load("")
	cfg.Name = "proj"
	if err := Resolve(cfg, Overrides{}); err != nil {
		t.Fatalf("Resolve() = %v, want nil", err)
	}
	st := s3Of(t, cfg)
	if st.Bucket != "env-bucket" || st.Endpoint != "env-endpoint" {
		t.Errorf("Storage=%+v", st)
	}
}

// An endpoint alone is enough; bucket falls back to its default.
func TestResolveInfersS3StorageFromEndpointAlone(t *testing.T) {
	t.Setenv(EnvBucket, "")
	t.Setenv(EnvEndpoint, "")

	cfg, _ := Load("")
	cfg.Name = "proj"
	err := Resolve(cfg, Overrides{
		Endpoint: Override{Value: "https://minio.example:9000", Set: true},
	})
	if err != nil {
		t.Fatalf("Resolve() = %v, want nil", err)
	}
	if st := s3Of(t, cfg); st.Bucket != DefaultBucket {
		t.Errorf("Bucket=%q, want %q", st.Bucket, DefaultBucket)
	}
}

// Inference must not fire on a bare invocation. Bucket has a built-in
// default, so triggering on the resolved value would fabricate a config
// pointing at a bucket the user never named.
func TestResolveDoesNotInferStorageWithoutSignal(t *testing.T) {
	t.Setenv(EnvBucket, "")
	t.Setenv(EnvEndpoint, "")

	cfg, _ := Load("")
	cfg.Name = "proj"
	err := Resolve(cfg, Overrides{})
	if !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("got %v, want ErrMissingRequired", err)
	}
	if !strings.Contains(err.Error(), "storage") {
		t.Errorf("error = %q, want it to name storage", err)
	}
}

// A storage block in the config file always wins; inference is a
// fallback, not an override.
func TestResolveDoesNotOverrideConfiguredStorage(t *testing.T) {
	t.Setenv(EnvBucket, "")
	t.Setenv(EnvEndpoint, "")

	cfg := &Config{
		Name: "proj",
		Storage: StorageField{BlobConfig: &FileConfig{
			ProviderConfig: ProviderConfig{Provider: "file"},
			DirPath:        "/mnt/backups",
		}},
	}
	// An ambient CONFKOFFER_BUCKET must not promote the block to S3.
	t.Setenv(EnvBucket, "from-the-environment")

	if err := Resolve(cfg, Overrides{}); err != nil {
		t.Fatalf("Resolve() = %v, want nil", err)
	}
	fc, ok := cfg.Storage.BlobConfig.(*FileConfig)
	if !ok {
		t.Fatalf("Storage is %T, want *FileConfig", cfg.Storage.BlobConfig)
	}
	if fc.DirPath != "/mnt/backups" {
		t.Errorf("DirPath=%q", fc.DirPath)
	}
}

// --bucket, --endpoint, and --region describe an S3 endpoint. Against a
// file, azure, or gcp block they used to be dropped on the floor, so
// `list --bucket wrong` reported the contents of the right bucket and
// the operator had no way to tell the flag had done nothing.
func TestResolveRejectsS3FlagsOnNonS3Provider(t *testing.T) {
	tests := []struct {
		name string
		ov   Overrides
		want string
	}{
		{"bucket", Overrides{Bucket: Override{Value: "b", Set: true}}, "--bucket"},
		{"endpoint", Overrides{Endpoint: Override{Value: "e", Set: true}}, "--endpoint"},
		{"region", Overrides{Region: Override{Value: "r", Set: true}}, "--region"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Name: "proj",
				Storage: StorageField{BlobConfig: &FileConfig{
					ProviderConfig: ProviderConfig{Provider: "file"},
					DirPath:        "/mnt/backups",
				}},
			}
			err := Resolve(cfg, tc.ov)
			if err == nil {
				t.Fatalf("Resolve() = nil, want an error naming %s", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %s", err, tc.want)
			}
			if !strings.Contains(err.Error(), "file") {
				t.Errorf("error = %q, want it to name the provider", err)
			}
		})
	}
}

// The env vars are ambient — a CONFKOFFER_BUCKET exported for another
// tool in the same shell must not turn every file-provider run into an
// error. Only an explicit flag is a statement about this invocation.
func TestResolveIgnoresS3EnvOnNonS3Provider(t *testing.T) {
	t.Setenv(EnvBucket, "some-bucket")
	t.Setenv(EnvEndpoint, "https://minio.example:9000")
	t.Setenv(EnvRegion, "eu-central-1")

	cfg := &Config{
		Name: "proj",
		Storage: StorageField{BlobConfig: &FileConfig{
			ProviderConfig: ProviderConfig{Provider: "file"},
			DirPath:        "/mnt/backups",
		}},
	}
	if err := Resolve(cfg, Overrides{}); err != nil {
		t.Fatalf("Resolve() = %v, want nil", err)
	}
}

func TestResolveMissingStorageIsExitTwo(t *testing.T) {
	// cleared so an ambient CONFKOFFER_BUCKET cannot trigger inference
	t.Setenv(EnvBucket, "")
	t.Setenv(EnvEndpoint, "")
	cfg, _ := Load("")
	cfg.Name = "proj"
	err := Resolve(cfg, Overrides{})
	if !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("got %v, want ErrMissingRequired", err)
	}
	if !strings.Contains(err.Error(), "storage") {
		t.Errorf("error = %q, want it to name storage", err)
	}
}

// The aws alias shares S3Config with minio/s3; this covers the
// aws-flavoured path and the region default in one go.
func TestResolveOverridesApplyToS3Config(t *testing.T) {
	t.Setenv(EnvRegion, "env-region")
	cfg := &Config{
		Name: "proj",
		Storage: StorageField{BlobConfig: &S3Config{
			ProviderConfig: ProviderConfig{Provider: "aws"},
			Bucket:         "yaml-bucket",
		}},
	}
	err := Resolve(cfg, Overrides{
		Bucket: Override{Value: "flag-bucket", Set: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	ac, ok := cfg.Storage.BlobConfig.(*S3Config)
	if !ok {
		t.Fatalf("Storage is %T, want *S3Config", cfg.Storage.BlobConfig)
	}
	if ac.Bucket != "flag-bucket" {
		t.Errorf("Bucket=%q, want flag-bucket", ac.Bucket)
	}
	if ac.Region != "env-region" {
		t.Errorf("Region=%q, want env-region", ac.Region)
	}
}

func TestResolveAWSDefaultsRegionAndBucket(t *testing.T) {
	t.Setenv(EnvBucket, "")
	t.Setenv(EnvRegion, "")
	cfg := &Config{
		Name: "proj",
		Storage: StorageField{BlobConfig: &S3Config{
			ProviderConfig: ProviderConfig{Provider: "aws"},
		}},
	}
	if err := Resolve(cfg, Overrides{}); err != nil {
		t.Fatal(err)
	}
	ac := cfg.Storage.BlobConfig.(*S3Config)
	if ac.Bucket != DefaultBucket {
		t.Errorf("Bucket=%q want %q", ac.Bucket, DefaultBucket)
	}
	if ac.Region != DefaultRegion {
		t.Errorf("Region=%q want %q", ac.Region, DefaultRegion)
	}
}

func TestResolvePasswordOverrideFromFlag(t *testing.T) {
	cfg, _ := Load("")
	cfg.Name = "proj"
	cfg.Storage = validStorage()

	err := Resolve(cfg, Overrides{
		Password: Override{Value: "from-flag", Set: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.PasswordOverride) != "from-flag" {
		t.Fatalf("PasswordOverride=%q", cfg.PasswordOverride)
	}
}

// CONFKOFFER_PASS is consumed by password.EnvSource at runtime, not by
// Resolve — keeps config concerns separate from the password subsystem.

func TestValidateName(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"proj", false},
		{"a", false},
		{"my-project", false},
		{"prod/aws/useast", false},
		{"marketing/mailchimp/prod", false},
		{"a-b-c/d-e-f/g", false},

		{"", true},
		{"/abs", true},
		{"trailing/", true},
		{"double//slash", true},
		{"UPPER", true},
		{"with space", true},
		{"a..b", true},
		{"a/../b", true},
		{"a/./b", true},
		{"-leading", true},
		{"trailing-", true},
		{"with_underscore", true},
		{"a/with_underscore/b", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			err := ValidateName(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateName(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
		})
	}
}

func TestPasswordSourceValidation(t *testing.T) {
	cases := []struct {
		name string
		p    Password
		ok   bool
	}{
		{"prompt", Password{Source: "prompt"}, true},
		{"env", Password{Source: "env"}, true},
		{"flag", Password{Source: "flag"}, true},
		{"pass-with-path", Password{Source: "pass", Pass: PassConfig{Path: "x/y"}}, true},
		{"pass-without-path", Password{Source: "pass"}, false},
		{"command-with-argv", Password{Source: "command", Command: CommandConfig{Argv: []string{"echo"}}}, true},
		{"command-without-argv", Password{Source: "command"}, false},
		{"unknown", Password{Source: "vault"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePasswordSource(tc.p)
			if (err != nil) != !tc.ok {
				t.Fatalf("got err=%v, ok=%v", err, tc.ok)
			}
		})
	}
}

func TestResolveCustomArgon2ParamsValidated(t *testing.T) {
	// Partial fill (time set, memory zero) is invalid and must be
	// rejected. A fully-zero struct is treated as "unset" and gets the
	// built-in defaults — that path is covered by TestResolveAppliesDefaults.
	cfg, _ := Load("")
	cfg.Name = "proj"
	cfg.Storage = validStorage()
	cfg.Crypto.Argon2id.Time = 1 // partial fill: memory still 0
	err := Resolve(cfg, Overrides{})
	if err == nil {
		t.Fatal("expected validation error for partial argon2id fill")
	}
}

// ---------------------------------------------------------------------
// storage: provider dispatch, key allowlist, and per-provider validation
// ---------------------------------------------------------------------

// storageNode parses body as a YAML mapping and returns the node the
// decoder would hand to StorageField.UnmarshalYAML.
func storageNode(t *testing.T, body string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if len(doc.Content) == 0 {
		t.Fatal("fixture: empty document")
	}
	return doc.Content[0]
}

func TestCreateStorageConfigProviders(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want BlobConfig
	}{
		{
			name: "aws",
			yaml: "provider: aws\nregion: eu-central-1\nbucket: my-backups\n",
			want: &S3Config{
				ProviderConfig: ProviderConfig{Provider: "aws"},
				Region:         "eu-central-1",
				Bucket:         "my-backups",
			},
		},
		{
			name: "aws with a FIPS endpoint override",
			yaml: "provider: aws\nbucket: b\nregion: us-east-1\nendpoint: s3-fips.us-east-1.amazonaws.com\n",
			want: &S3Config{
				ProviderConfig: ProviderConfig{Provider: "aws"},
				Bucket:         "b",
				Region:         "us-east-1",
				Endpoint:       "s3-fips.us-east-1.amazonaws.com",
			},
		},
		{
			name: "s3 alias for a non-AWS vendor",
			yaml: "provider: s3\nbucket: b\nendpoint: object.storage.example.de\nregion: eu01\n",
			want: &S3Config{
				ProviderConfig: ProviderConfig{Provider: "s3"},
				Bucket:         "b",
				Region:         "eu01",
				Endpoint:       "object.storage.example.de",
			},
		},
		{
			name: "azure",
			yaml: "provider: azure\ncontainerid: mycontainer\n",
			want: &AzureConfig{
				ProviderConfig: ProviderConfig{Provider: "azure"},
				ContainerID:    "mycontainer",
			},
		},
		{
			name: "file",
			yaml: "provider: file\ndirpath: /mnt/backups/confkoffer\n",
			want: &FileConfig{
				ProviderConfig: ProviderConfig{Provider: "file"},
				DirPath:        "/mnt/backups/confkoffer",
			},
		},
		{
			name: "gcp",
			yaml: "provider: gcp\nbucket: my-backups\n",
			want: &GCPConfig{
				ProviderConfig: ProviderConfig{Provider: "gcp"},
				Bucket:         "my-backups",
			},
		},
		{
			name: "gcp with universe domain",
			yaml: "provider: gcp\nbucket: my-backups\nuniverse_domain: example.sovereign\n",
			want: &GCPConfig{
				ProviderConfig: ProviderConfig{Provider: "gcp"},
				Bucket:         "my-backups",
				UniverseDomain: "example.sovereign",
			},
		},
		{
			name: "minio without insecure defaults to secure",
			yaml: "provider: minio\nbucket: confkoffer\nregion: us-east-1\nendpoint: localhost:9000\n",
			want: &S3Config{
				ProviderConfig: ProviderConfig{Provider: "minio"},
				Bucket:         "confkoffer",
				Region:         "us-east-1",
				Endpoint:       "localhost:9000",
				Insecure:       false,
			},
		},
		{
			name: "minio with insecure true",
			yaml: "provider: minio\nbucket: confkoffer\nendpoint: localhost:9000\ninsecure: true\n",
			want: &S3Config{
				ProviderConfig: ProviderConfig{Provider: "minio"},
				Bucket:         "confkoffer",
				Endpoint:       "localhost:9000",
				Insecure:       true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CreateStorageConfig(storageNode(t, tc.yaml))
			if err != nil {
				t.Fatalf("CreateStorageConfig() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("CreateStorageConfig() =\n  %#v\nwant\n  %#v", got, tc.want)
			}
			if got.GetProvider() != tc.want.GetProvider() {
				t.Errorf("GetProvider() = %q, want %q", got.GetProvider(), tc.want.GetProvider())
			}
		})
	}
}

func TestCreateStorageConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "missing provider",
			yaml: "bucket: b\n",
			want: "missing the provider key",
		},
		{
			name: "empty provider",
			yaml: "provider: \"\"\nbucket: b\n",
			want: "missing the provider key",
		},
		{
			name: "unknown provider",
			yaml: "provider: dropbox\n",
			want: `invalid provider "dropbox"`,
		},
		{
			name: "wrong type for known key",
			yaml: "provider: minio\ninsecure: not-a-bool\n",
			want: "storage (minio)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CreateStorageConfig(storageNode(t, tc.yaml))
			if err == nil {
				t.Fatalf("CreateStorageConfig() = %#v, want error", got)
			}
			if got != nil {
				t.Errorf("CreateStorageConfig() = %#v, want nil alongside error", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

// The storage node is decoded separately from the main document, so
// KnownFields(true) never sees inside it. checkStorageKeys restores that
// strictness per provider.
func TestCreateStorageConfigRejectsUnknownKeys(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		badKey  string
		allowed []string // must be listed in the error
	}{
		{
			name:    "typo",
			yaml:    "provider: minio\nbucket: b\nendpoint: e\nbukket: typo\n",
			badKey:  "bukket",
			allowed: []string{"bucket", "endpoint", "insecure", "provider", "region"},
		},
		{
			name:    "key belonging to another provider",
			yaml:    "provider: file\ndirpath: /mnt/b\nbucket: nope\n",
			badKey:  "bucket",
			allowed: []string{"dirpath", "provider"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CreateStorageConfig(storageNode(t, tc.yaml))
			if err == nil {
				t.Fatal("want error for unknown key")
			}
			if !strings.Contains(err.Error(), tc.badKey) {
				t.Errorf("error = %q, want it to name %q", err, tc.badKey)
			}
			for _, k := range tc.allowed {
				if !strings.Contains(err.Error(), k) {
					t.Errorf("error = %q, want it to list allowed key %q", err, k)
				}
			}
		})
	}
}

// The allowlist is derived from the struct's yaml tags, so every
// declared key must be accepted.
func TestCreateStorageConfigAcceptsEveryDeclaredKey(t *testing.T) {
	cases := []string{
		"provider: aws\nbucket: b\nregion: r\nendpoint: e\ninsecure: true\n",
		"provider: s3\nbucket: b\nendpoint: e\n",
		"provider: azure\ncontainerid: c\n",
		"provider: file\ndirpath: /mnt/b\n",
		"provider: gcp\nbucket: b\nuniverse_domain: d\n",
		"provider: minio\nbucket: b\nendpoint: e\nregion: r\ninsecure: true\n",
	}
	for _, y := range cases {
		if _, err := CreateStorageConfig(storageNode(t, y)); err != nil {
			t.Errorf("%q: unexpected error: %v", y, err)
		}
	}
}

func TestCreateStorageConfigRejectsNonMapping(t *testing.T) {
	if _, err := CreateStorageConfig(storageNode(t, "- provider: aws\n")); err == nil {
		t.Fatal("want error for a sequence node")
	}
}

func TestCreateStorageConfigNilNode(t *testing.T) {
	if _, err := CreateStorageConfig(nil); err == nil {
		t.Fatal("want error for nil node")
	}
}

// Unknown-provider errors must name the offending value. Guards against
// the %s-verb-without-argument bug that printf-style formatting invites.
func TestCreateStorageConfigUnknownProviderIsInterpolated(t *testing.T) {
	_, err := CreateStorageConfig(storageNode(t, "provider: dropbox\n"))
	if err == nil {
		t.Fatal("want error for unknown provider")
	}
	if strings.Contains(err.Error(), "%!") {
		t.Errorf("error has an unfilled format verb: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "dropbox") {
		t.Errorf("error = %q, want it to name the offending provider", err.Error())
	}
}

func TestBlobConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  BlobConfig
		want []string // substrings; empty means Validate must succeed
	}{
		{
			// endpoint is optional for aws: absent means AWS S3 itself
			name: "aws ok without endpoint",
			cfg:  &S3Config{ProviderConfig: ProviderConfig{Provider: "aws"}, Bucket: "b"},
		},
		{
			name: "aws ok with endpoint override",
			cfg: &S3Config{
				ProviderConfig: ProviderConfig{Provider: "aws"},
				Bucket:         "b", Endpoint: "s3-fips.us-east-1.amazonaws.com",
			},
		},
		{
			name: "aws missing bucket",
			cfg:  &S3Config{ProviderConfig: ProviderConfig{Provider: "aws"}},
			want: []string{"aws", "bucket"},
		},
		{
			// a non-aws alias must not silently fall back to AWS
			name: "s3 missing endpoint",
			cfg:  &S3Config{ProviderConfig: ProviderConfig{Provider: "s3"}, Bucket: "b"},
			want: []string{"s3", "endpoint"},
		},
		{
			name: "azure ok",
			cfg: &AzureConfig{
				ProviderConfig: ProviderConfig{Provider: "azure"},
				ContainerID:    "c",
			},
		},
		{
			name: "azure missing containerid",
			cfg:  &AzureConfig{ProviderConfig: ProviderConfig{Provider: "azure"}},
			want: []string{"azure", "containerid"},
		},
		{
			name: "file ok",
			cfg:  &FileConfig{ProviderConfig: ProviderConfig{Provider: "file"}, DirPath: "/mnt/b"},
		},
		{
			name: "file missing dirpath",
			cfg:  &FileConfig{ProviderConfig: ProviderConfig{Provider: "file"}},
			want: []string{"file", "dirpath"},
		},
		{
			name: "file blank dirpath is missing",
			cfg:  &FileConfig{ProviderConfig: ProviderConfig{Provider: "file"}, DirPath: "   "},
			want: []string{"file", "dirpath"},
		},
		{
			name: "gcp ok",
			cfg:  &GCPConfig{ProviderConfig: ProviderConfig{Provider: "gcp"}, Bucket: "b"},
		},
		{
			// universe_domain is optional
			name: "gcp ok without universe domain",
			cfg:  &GCPConfig{ProviderConfig: ProviderConfig{Provider: "gcp"}, Bucket: "b"},
		},
		{
			name: "gcp missing bucket",
			cfg:  &GCPConfig{ProviderConfig: ProviderConfig{Provider: "gcp"}},
			want: []string{"gcp", "bucket"},
		},
		{
			name: "minio ok",
			cfg: &S3Config{
				ProviderConfig: ProviderConfig{Provider: "minio"},
				Bucket:         "b", Endpoint: "e",
			},
		},
		{
			name: "minio missing endpoint",
			cfg:  &S3Config{ProviderConfig: ProviderConfig{Provider: "minio"}, Bucket: "b"},
			want: []string{"minio", "endpoint"},
		},
		{
			// region is optional for every S3 alias
			name: "minio without region is valid",
			cfg: &S3Config{
				ProviderConfig: ProviderConfig{Provider: "minio"},
				Bucket:         "b", Endpoint: "e",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !errors.Is(err, ErrMissingRequired) {
				t.Errorf("Validate() = %v, want it to wrap ErrMissingRequired", err)
			}
			for _, sub := range tc.want {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), sub)
				}
			}
		})
	}
}

// The whole storage block decodes through StorageField.UnmarshalYAML,
// so Config needs no separate wire-format struct.
func TestStorageFieldDecodesThroughConfig(t *testing.T) {
	const doc = `
name: proj
storage:
  provider: file
  dirpath: /mnt/backups
`
	var cfg Config
	d := yaml.NewDecoder(strings.NewReader(doc))
	d.KnownFields(true)
	if err := d.Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	fc, ok := cfg.Storage.BlobConfig.(*FileConfig)
	if !ok {
		t.Fatalf("Storage is %T, want *FileConfig", cfg.Storage.BlobConfig)
	}
	if fc.DirPath != "/mnt/backups" {
		t.Errorf("DirPath=%q", fc.DirPath)
	}
	// promoted through the embedded interface
	if cfg.Storage.GetProvider() != "file" {
		t.Errorf("GetProvider()=%q, want file", cfg.Storage.GetProvider())
	}
}

// yaml.v3 understands time.Duration natively, so "10s" needs no
// string-then-ParseDuration step.
func TestCommandTimeoutDecodesAsDuration(t *testing.T) {
	const doc = `
name: proj
storage:
  provider: file
  dirpath: /mnt/b
password:
  source: command
  command:
    argv: ["vault", "kv", "get", "-field=passphrase", "secret/x"]
    timeout: 10s
`
	var cfg Config
	d := yaml.NewDecoder(strings.NewReader(doc))
	d.KnownFields(true)
	if err := d.Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Password.Command.Timeout != 10*time.Second {
		t.Errorf("Timeout=%v, want 10s", cfg.Password.Command.Timeout)
	}
	if len(cfg.Password.Command.Argv) != 5 {
		t.Errorf("Argv=%v", cfg.Password.Command.Argv)
	}
}

func TestCommandTimeoutRejectsUnitlessValue(t *testing.T) {
	const doc = "password:\n  command:\n    timeout: 30\n"
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err == nil {
		t.Fatalf("want error for unitless duration, got Timeout=%v", cfg.Password.Command.Timeout)
	}
}

// Domain-only fields carry `yaml:"-"` so a config file cannot set them.
// Without the tag KnownFields(true) accepts them, because the fields do
// exist on the struct.
func TestDomainOnlyFieldsAreNotSettableFromYAML(t *testing.T) {
	for _, doc := range []string{
		"sourcepath: /etc/evil\n",
		"passwordoverride: hunter2\n",
	} {
		var cfg Config
		d := yaml.NewDecoder(strings.NewReader(doc))
		d.KnownFields(true)
		err := d.Decode(&cfg)
		if err == nil {
			t.Errorf("%q: decoded without error; want 'field not found'", strings.TrimSpace(doc))
		}
		if cfg.SourcePath != "" {
			t.Errorf("%q: SourcePath=%q, want empty", strings.TrimSpace(doc), cfg.SourcePath)
		}
		if len(cfg.PasswordOverride) != 0 {
			t.Errorf("%q: PasswordOverride=%q, want empty", strings.TrimSpace(doc), cfg.PasswordOverride)
		}
	}
}
