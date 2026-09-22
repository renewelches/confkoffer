package store

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob"

	"github.com/renewelches/confkoffer/internal/config"
)

func pcfg(p string) config.ProviderConfig { return config.ProviderConfig{Provider: p} }

func TestBucketURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.BlobConfig
		want string
	}{
		{
			name: "aws without endpoint",
			cfg: &config.S3Config{
				ProviderConfig: pcfg("aws"),
				Bucket:         "confkoffer",
				Region:         "eu-central-1",
			},
			want: "s3://confkoffer?region=eu-central-1",
		},
		{
			name: "aws without region",
			cfg:  &config.S3Config{ProviderConfig: pcfg("aws"), Bucket: "confkoffer"},
			want: "s3://confkoffer",
		},
		{
			name: "self-hosted s3 over https",
			cfg: &config.S3Config{
				ProviderConfig: pcfg("s3"),
				Bucket:         "confkoffer",
				Region:         "eu01",
				Endpoint:       "object.storage.example.de",
			},
			want: "s3://confkoffer?endpoint=https%3A%2F%2Fobject.storage.example.de&region=eu01&use_path_style=true",
		},
		{
			name: "local minio over http",
			cfg: &config.S3Config{
				ProviderConfig: pcfg("minio"),
				Bucket:         "confkoffer",
				Region:         "us-east-1",
				Endpoint:       "localhost:9000",
				Insecure:       true,
			},
			want: "s3://confkoffer?endpoint=http%3A%2F%2Flocalhost%3A9000&region=us-east-1&use_path_style=true",
		},
		{
			name: "azure",
			cfg: &config.AzureConfig{
				ProviderConfig: pcfg("azure"),
				ContainerID:    "mycontainer",
			},
			want: "azblob://mycontainer",
		},
		{
			name: "gcp",
			cfg: &config.GCPConfig{
				ProviderConfig: pcfg("gcp"),
				Bucket:         "my-backups",
			},
			want: "gs://my-backups",
		},
		{
			name: "gcp in a sovereign universe",
			cfg: &config.GCPConfig{
				ProviderConfig: pcfg("gcp"),
				Bucket:         "my-backups",
				UniverseDomain: "example.sovereign",
			},
			want: "gs://my-backups?universe_domain=example.sovereign",
		},
		{
			name: "file",
			cfg: &config.FileConfig{
				ProviderConfig: pcfg("file"),
				DirPath:        "/mnt/backups/confkoffer",
			},
			want: "file:///mnt/backups/confkoffer?create_dir=true",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bucketURL(tc.cfg)
			if err != nil {
				t.Fatalf("bucketURL() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("bucketURL() =\n  %s\nwant\n  %s", got, tc.want)
			}
			if _, err := url.Parse(got); err != nil {
				t.Errorf("result does not parse as a URL: %v", err)
			}
		})
	}
}

func TestBucketURLErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.BlobConfig
		want string
	}{
		{
			name: "s3 without bucket",
			cfg:  &config.S3Config{ProviderConfig: pcfg("aws")},
			want: "bucket is required",
		},
		{
			name: "azure without container",
			cfg:  &config.AzureConfig{ProviderConfig: pcfg("azure")},
			want: "containerid is required",
		},
		{
			name: "file without dirpath",
			cfg:  &config.FileConfig{ProviderConfig: pcfg("file")},
			want: "dirpath is required",
		},
		{
			name: "file with relative dirpath",
			cfg:  &config.FileConfig{ProviderConfig: pcfg("file"), DirPath: "relative/path"},
			want: "must be absolute",
		},
		{
			name: "gcp without bucket",
			cfg:  &config.GCPConfig{ProviderConfig: pcfg("gcp")},
			want: "bucket is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bucketURL(tc.cfg)
			if err == nil {
				t.Fatalf("bucketURL() = %q, want error", got)
			}
			if got != "" {
				t.Errorf("bucketURL() = %q, want empty string alongside error", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// Only emit query parameters the drivers document. gocloud rejects
// unknown ones with "unknown query parameter", which would surface as a
// connection failure rather than a config error.
func TestBucketURLEmitsOnlyKnownParams(t *testing.T) {
	known := map[string]map[string]bool{
		"s3":     {"region": true, "endpoint": true, "use_path_style": true},
		"azblob": {},
		"gs":     {"universe_domain": true},
		"file":   {"create_dir": true},
	}
	cfgs := []config.BlobConfig{
		&config.S3Config{ProviderConfig: pcfg("minio"), Bucket: "b", Region: "r", Endpoint: "h:9000", Insecure: true},
		&config.AzureConfig{ProviderConfig: pcfg("azure"), ContainerID: "c"},
		&config.GCPConfig{ProviderConfig: pcfg("gcp"), Bucket: "b", UniverseDomain: "d"},
		&config.FileConfig{ProviderConfig: pcfg("file"), DirPath: "/mnt/b"},
	}
	for _, c := range cfgs {
		raw, err := bucketURL(c)
		if err != nil {
			t.Fatalf("%T: %v", c, err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("%T: %v", c, err)
		}
		for k := range u.Query() {
			if !known[u.Scheme][k] {
				t.Errorf("%s: emits undocumented query parameter %q", u.Scheme, k)
			}
		}
	}
}

// End-to-end for the one driver that needs no credentials: the URL we
// generate must actually open, and create_dir must create the directory.
func TestFileURLOpensRealBucket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist-yet")
	raw, err := bucketURL(&config.FileConfig{
		ProviderConfig: pcfg("file"),
		DirPath:        dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	bucket, err := blob.OpenBucket(ctx, raw)
	if err != nil {
		t.Fatalf("OpenBucket(%q) = %v", raw, err)
	}
	defer bucket.Close()

	const key = "proj/2026-04-28T12-34-56Z-7d4e.enc"
	if err := bucket.WriteAll(ctx, key, []byte("ciphertext"), nil); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	got, err := bucket.ReadAll(ctx, key)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "ciphertext" {
		t.Errorf("ReadAll = %q", got)
	}
}
