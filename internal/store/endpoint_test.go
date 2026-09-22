package store

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/renewelches/confkoffer/internal/config"
)

// These paths moved here from the minio-era s3.go and arrived without
// their tests. They decide whether credentials travel over TLS, so they
// are the last place in the package that should go unexercised.

func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		forced     bool
		wantHost   string
		wantSecure bool
	}{
		{"bare host defaults to TLS", "minio.example:9000", false, "minio.example:9000", true},
		{"bare host honours insecure", "minio.example:9000", true, "minio.example:9000", false},
		{"https is stripped and stays secure", "https://minio.example:9000", false, "minio.example:9000", true},
		{"http is stripped and is insecure", "http://minio.example:9000", false, "minio.example:9000", false},
		{"scheme match is case-insensitive", "HTTPS://minio.example", false, "minio.example", true},
		{"http uppercase is still insecure", "HTTP://minio.example", false, "minio.example", false},
		{"surrounding space is trimmed", "  minio.example  ", false, "minio.example", true},
		{"path survives the strip", "https://gw.example/s3", false, "gw.example/s3", true},
		{"bracketed ipv6 keeps its brackets", "http://[::1]:9000", false, "[::1]:9000", false},

		// An explicit https:// outranks insecure: the user named the
		// scheme on the endpoint itself, which is the more specific
		// statement. Downgrading it would silently put credentials in
		// cleartext against a host the config said to reach over TLS.
		{"explicit https outranks insecure", "https://minio.example", true, "minio.example", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, secure, err := normalizeEndpoint(tc.in, tc.forced)
			if err != nil {
				t.Fatalf("normalizeEndpoint(%q, %v) error = %v", tc.in, tc.forced, err)
			}
			if host != tc.wantHost {
				t.Errorf("host = %q, want %q", host, tc.wantHost)
			}
			if secure != tc.wantSecure {
				t.Errorf("secure = %v, want %v", secure, tc.wantSecure)
			}
		})
	}
}

func TestNormalizeEndpointRejectsEmpty(t *testing.T) {
	for _, in := range []string{"", "   "} {
		if _, _, err := normalizeEndpoint(in, false); err == nil {
			t.Errorf("normalizeEndpoint(%q) = nil error, want error", in)
		}
	}
}

// captureWarn swaps in a slog handler writing to a buffer for the
// duration of fn, and returns what was logged.
func captureWarn(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	return buf.String()
}

// The warning is the only signal a user gets that credentials are about
// to cross the wire in cleartext, so its presence is behaviour, not
// decoration.
func TestWarnInsecureFiresForRemoteHosts(t *testing.T) {
	for _, host := range []string{"minio.example:9000", "10.0.0.5:9000", "s3.internal"} {
		out := captureWarn(t, func() { warnInsecure(host) })
		if !strings.Contains(out, "TLS disabled") {
			t.Errorf("warnInsecure(%q) logged %q, want a TLS-disabled warning", host, out)
		}
	}
}

// Cleartext to a loopback MinIO is the documented local workflow. A
// warning there is noise on every single run, which is how users learn
// to ignore the warning that matters.
func TestWarnInsecureSkipsLoopback(t *testing.T) {
	for _, host := range []string{"localhost:9000", "localhost", "127.0.0.1:9000", "[::1]:9000"} {
		out := captureWarn(t, func() { warnInsecure(host) })
		if out != "" {
			t.Errorf("warnInsecure(%q) logged %q, want silence for loopback", host, out)
		}
	}
}

func TestValidateUrlS3(t *testing.T) {
	cases := []struct {
		name         string
		in           config.S3Config
		wantEndpoint string
		wantInsecure bool
	}{
		{
			name:         "http endpoint records insecure transport",
			in:           config.S3Config{Endpoint: "http://minio.example:9000"},
			wantEndpoint: "minio.example:9000",
			wantInsecure: true,
		},
		{
			// Regression guard: assigning normalizeEndpoint's `secure`
			// return straight onto Insecure inverts the meaning and
			// downgrades every https endpoint to cleartext.
			name:         "https endpoint stays secure",
			in:           config.S3Config{Endpoint: "https://minio.example:9000"},
			wantEndpoint: "minio.example:9000",
			wantInsecure: false,
		},
		{
			name:         "insecure flag applies to a bare host",
			in:           config.S3Config{Endpoint: "minio.example:9000", Insecure: true},
			wantEndpoint: "minio.example:9000",
			wantInsecure: true,
		},
		{
			// An empty endpoint means AWS S3 proper, not a malformed
			// config — normalizing it would fail on "empty endpoint".
			name:         "empty endpoint is left alone",
			in:           config.S3Config{ProviderConfig: config.ProviderConfig{Provider: "aws"}},
			wantEndpoint: "",
			wantInsecure: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in
			if err := validateUrl(&got); err != nil {
				t.Fatalf("validateUrl() error = %v", err)
			}
			if got.Endpoint != tc.wantEndpoint {
				t.Errorf("Endpoint = %q, want %q", got.Endpoint, tc.wantEndpoint)
			}
			if got.Insecure != tc.wantInsecure {
				t.Errorf("Insecure = %v, want %v", got.Insecure, tc.wantInsecure)
			}
		})
	}
}

// validateUrl mutates the config in place and New calls it, so a second
// pass over an already-normalized config must not drift — most visibly
// by re-flipping Insecure once the scheme has been stripped away.
func TestValidateUrlIsIdempotent(t *testing.T) {
	for _, endpoint := range []string{"http://minio.example:9000", "https://minio.example:9000", "minio.example:9000"} {
		c := &config.S3Config{Endpoint: endpoint}
		if err := validateUrl(c); err != nil {
			t.Fatalf("first pass: %v", err)
		}
		first := *c
		if err := validateUrl(c); err != nil {
			t.Fatalf("second pass: %v", err)
		}
		if *c != first {
			t.Errorf("endpoint %q: second pass changed %+v to %+v", endpoint, first, *c)
		}
	}
}

func TestValidateUrlAzure(t *testing.T) {
	t.Run("strips the optional scheme", func(t *testing.T) {
		c := &config.AzureConfig{ContainerID: "azblob://backups"}
		if err := validateUrl(c); err != nil {
			t.Fatalf("validateUrl() error = %v", err)
		}
		if c.ContainerID != "backups" {
			t.Errorf("ContainerID = %q, want %q", c.ContainerID, "backups")
		}
	})
	// gocloud puts the container in the URL host, which cannot hold a
	// path — a slash means an account or path was pasted in.
	t.Run("rejects a path", func(t *testing.T) {
		c := &config.AzureConfig{ContainerID: "myaccount/backups"}
		err := validateUrl(c)
		if err == nil {
			t.Fatal("validateUrl() = nil error, want error")
		}
		if !strings.Contains(err.Error(), "not a path") {
			t.Errorf("err = %q, want it to mention the path", err)
		}
	})
	t.Run("rejects empty", func(t *testing.T) {
		if err := validateUrl(&config.AzureConfig{ContainerID: "  "}); err == nil {
			t.Error("validateUrl() = nil error, want error")
		}
	})
}

func TestValidateUrlFile(t *testing.T) {
	t.Run("strips the scheme and cleans the path", func(t *testing.T) {
		c := &config.FileConfig{DirPath: "file:///mnt/backups/../backups/confkoffer/"}
		if err := validateUrl(c); err != nil {
			t.Fatalf("validateUrl() error = %v", err)
		}
		if c.DirPath != "/mnt/backups/confkoffer" {
			t.Errorf("DirPath = %q, want %q", c.DirPath, "/mnt/backups/confkoffer")
		}
	})
	// A relative path resolves against the working directory, so the
	// same config would name a different destination depending on where
	// confkoffer was run. For a backup target that silently loses
	// snapshots.
	t.Run("rejects a relative path", func(t *testing.T) {
		if err := validateUrl(&config.FileConfig{DirPath: "./backups"}); err == nil {
			t.Error("validateUrl() = nil error, want error")
		}
	})
	t.Run("rejects empty", func(t *testing.T) {
		if err := validateUrl(&config.FileConfig{DirPath: "  "}); err == nil {
			t.Error("validateUrl() = nil error, want error")
		}
	})
}

// GCS is addressed as gs://<bucket>: no scheme, endpoint, or region to
// normalize. Presence of the bucket is Validate's job.
func TestValidateUrlGCPIsANoOp(t *testing.T) {
	c := &config.GCPConfig{Bucket: "backups", UniverseDomain: "example.com"}
	before := *c
	if err := validateUrl(c); err != nil {
		t.Fatalf("validateUrl() error = %v", err)
	}
	if *c != before {
		t.Errorf("validateUrl changed %+v to %+v", before, *c)
	}
}

// The scheme on the endpoint outranks the `insecure` key in both
// directions. Documented in the normalizeEndpoint comment and in the
// README's provider reference; pinned here so neither can drift.
func TestEndpointSchemeOutranksInsecure(t *testing.T) {
	tests := []struct {
		endpoint     string
		insecure     bool
		wantInsecure bool
		why          string
	}{
		{"https://minio.example", true, false, "explicit https must not be downgraded by insecure"},
		{"http://minio.example", false, true, "explicit http is honoured even with insecure unset"},
		{"minio.example", true, true, "a bare host lets insecure decide"},
		{"minio.example", false, false, "a bare host defaults to TLS"},
	}
	for _, tc := range tests {
		t.Run(tc.endpoint+"/"+tc.why, func(t *testing.T) {
			c := &config.S3Config{Endpoint: tc.endpoint, Insecure: tc.insecure}
			if err := validateUrl(c); err != nil {
				t.Fatalf("validateUrl() error = %v", err)
			}
			if c.Insecure != tc.wantInsecure {
				t.Errorf("Insecure = %v, want %v — %s", c.Insecure, tc.wantInsecure, tc.why)
			}
		})
	}
}
