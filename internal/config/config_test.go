package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

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
	if cfg.Storage.Bucket != "my-backups" || cfg.Storage.Endpoint != "s3.example.com" || cfg.Storage.Region != "eu-central-1" {
		t.Errorf("Storage=%+v", cfg.Storage)
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
  bukket: typo
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error on unknown key 'bukket'")
	}
}

func TestResolveAppliesDefaults(t *testing.T) {
	t.Setenv(EnvName, "")
	t.Setenv(EnvBucket, "")
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvRegion, "")

	cfg, _ := Load("")
	cfg.Name = "proj"
	cfg.Storage.Endpoint = "s3.example.com"

	if err := Resolve(cfg, Overrides{}); err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Bucket != DefaultBucket {
		t.Errorf("Bucket=%q want %q", cfg.Storage.Bucket, DefaultBucket)
	}
	if cfg.Storage.Region != DefaultRegion {
		t.Errorf("Region=%q want %q", cfg.Storage.Region, DefaultRegion)
	}
}

func TestResolvePrecedenceFlagBeatsEnvBeatsYAMLBeatsDefault(t *testing.T) {
	// flag wins
	cfg := &Config{Name: "yaml", Storage: Storage{Bucket: "yaml-bucket", Endpoint: "yaml-ep"}}
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
	if cfg.Storage.Bucket != "flag-bucket" {
		t.Errorf("Bucket=%q, want flag-bucket", cfg.Storage.Bucket)
	}
	// env wins for endpoint (no flag override)
	if cfg.Storage.Endpoint != "env-ep" {
		t.Errorf("Endpoint=%q, want env-ep", cfg.Storage.Endpoint)
	}
}

func TestResolveEnvBeatsYAML(t *testing.T) {
	cfg := &Config{Name: "yaml", Storage: Storage{Bucket: "yaml-bucket", Endpoint: "yaml-ep"}}
	t.Setenv(EnvBucket, "env-bucket")

	if err := Resolve(cfg, Overrides{}); err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Bucket != "env-bucket" {
		t.Errorf("Bucket=%q, want env-bucket", cfg.Storage.Bucket)
	}
}

func TestResolveYAMLBeatsDefault(t *testing.T) {
	cfg := &Config{Name: "yaml", Storage: Storage{Bucket: "yaml-bucket", Endpoint: "yaml-ep"}}
	if err := Resolve(cfg, Overrides{}); err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Bucket != "yaml-bucket" {
		t.Errorf("Bucket=%q, want yaml-bucket", cfg.Storage.Bucket)
	}
}

func TestResolveMissingNameIsExitTwo(t *testing.T) {
	cfg, _ := Load("")
	cfg.Storage.Endpoint = "s3.example.com"
	err := Resolve(cfg, Overrides{})
	if !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("got %v, want ErrMissingRequired", err)
	}
}

func TestResolveMissingEndpointIsExitTwo(t *testing.T) {
	cfg, _ := Load("")
	cfg.Name = "proj"
	err := Resolve(cfg, Overrides{})
	if !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("got %v, want ErrMissingRequired", err)
	}
}

func TestResolvePasswordOverrideFromFlag(t *testing.T) {
	cfg, _ := Load("")
	cfg.Name = "proj"
	cfg.Storage.Endpoint = "s3.example.com"

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
	cfg.Storage.Endpoint = "s3.example.com"
	cfg.Crypto.Argon2id.Time = 1 // partial fill: memory still 0
	err := Resolve(cfg, Overrides{})
	if err == nil {
		t.Fatal("expected validation error for partial argon2id fill")
	}
}
