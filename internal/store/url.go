package store

import (
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/renewelches/confkoffer/internal/config"
)

// bucketURL renders cfg as a gocloud.dev/blob URL suitable for
// blob.OpenBucket.
//
// This is the only place that knows gocloud's URL contract. The config
// package deliberately does not: query parameters like use_path_style
// and create_dir are decisions about how to drive the driver, not
// settings the user expressed, and their spellings follow gocloud's
// release cycle rather than confkoffer's schema. Keeping the mapping
// here means a driver or library change touches one file.
//
// gocloud rejects query parameters it does not recognise, so only
// documented ones are emitted.
func bucketURL(cfg config.BlobConfig) (string, error) {
	switch c := cfg.(type) {
	case *config.S3Config:
		return s3URL(c)
	case *config.AzureConfig:
		return azureURL(c)
	case *config.FileConfig:
		return fileURL(c)
	case *config.GCPConfig:
		return gcpURL(c)
	default:
		return "", fmt.Errorf("store: unsupported storage configuration %T", cfg)
	}
}

// s3URL builds "s3://<bucket>" with the region and, for non-AWS stores,
// a custom endpoint.
func s3URL(c *config.S3Config) (string, error) {
	if c.Bucket == "" {
		return "", fmt.Errorf("store (%s): bucket is required", c.GetProvider())
	}
	u := url.URL{Scheme: "s3", Host: c.Bucket}
	q := url.Values{}
	if c.Region != "" {
		q.Set("region", c.Region)
	}
	if c.Endpoint != "" {
		// validateUrl strips the scheme and records it as Insecure, but
		// gocloud's endpoint parameter wants a full URL — put it back.
		scheme := "https://"
		if c.Insecure {
			scheme = "http://"
		}
		q.Set("endpoint", scheme+c.Endpoint)
		// Self-hosted S3 implementations generally do not serve
		// virtual-host addressing (bucket.host), so ask for path style
		// (host/bucket). AWS accepts both; MinIO and Ceph need this.
		q.Set("use_path_style", "true")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// azureURL builds "azblob://<container>".
//
// gocloud uses the URL host as the container name. The storage account
// and its region are not part of the URL — the account comes from
// AZURE_STORAGE_ACCOUNT with the credentials, and its region is fixed
// when the account is created. See the note on config.AzureConfig.
func azureURL(c *config.AzureConfig) (string, error) {
	if c.ContainerID == "" {
		return "", fmt.Errorf("store (%s): containerid is required", c.GetProvider())
	}
	u := url.URL{Scheme: "azblob", Host: c.ContainerID}
	return u.String(), nil
}

// fileURL builds "file:///abs/path?create_dir=true".
//
// create_dir makes the first pack to a fresh mount succeed instead of
// failing on a missing directory.
func fileURL(c *config.FileConfig) (string, error) {
	if c.DirPath == "" {
		return "", fmt.Errorf("store (%s): dirpath is required", c.GetProvider())
	}
	// Relative paths resolve against the process working directory, so
	// the same config would name different destinations depending on
	// where confkoffer ran. validateUrl rejects these too; this is a
	// second line of defence for callers that skip it.
	if !filepath.IsAbs(c.DirPath) {
		return "", fmt.Errorf("store (%s): dirpath %q must be absolute", c.GetProvider(), c.DirPath)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(c.DirPath)}
	q := url.Values{}
	q.Set("create_dir", "true")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// gcpURL builds "gs://<bucket>".
//
// No region is emitted: gcsblob has no region or location parameter,
// because a GCS bucket's location is fixed at creation and gs://<bucket>
// resolves globally. See the note on config.GCPConfig.
func gcpURL(c *config.GCPConfig) (string, error) {
	if c.Bucket == "" {
		return "", fmt.Errorf("store (%s): bucket is required", c.GetProvider())
	}
	u := url.URL{Scheme: "gs", Host: c.Bucket}
	if c.UniverseDomain != "" {
		q := url.Values{}
		q.Set("universe_domain", c.UniverseDomain)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}
