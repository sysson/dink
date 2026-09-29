package blobstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// Config names one Go CDK blob driver and its settings, e.g.
//
//	{"file": {"path": "/var/lib/dinki/blobs"}}
//	{"s3": {"bucket": "dinki", "region": "eu-west-1"}}
//
// Cloud credentials come from each provider's standard environment
// (AWS_*, GOOGLE_APPLICATION_CREDENTIALS, AZURE_*), not from this config.
type Config struct {
	File  *FileConfig  `json:"file,omitempty"`
	Mem   *MemConfig   `json:"mem,omitempty"`
	S3    *S3Config    `json:"s3,omitempty"`
	GCS   *GCSConfig   `json:"gcs,omitempty"`
	Azure *AzureConfig `json:"azure,omitempty"`
}

// Driver is one blob driver's settings.
type Driver interface {
	Validate() error
	// URL returns the Go CDK bucket URL for the settings.
	URL() string
}

// UnmarshalJSON replaces c rather than merging into it, so a configured
// driver replaces the default one instead of adding a second.
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*c = Config(decoded)
	return nil
}

// Driver returns the name and settings of the selected driver.
func (c Config) Driver() (string, Driver, error) {
	var names []string
	var selected Driver
	add := func(name string, driver Driver) {
		names = append(names, name)
		selected = driver
	}
	if c.File != nil {
		add("file", *c.File)
	}
	if c.Mem != nil {
		add("mem", *c.Mem)
	}
	if c.S3 != nil {
		add("s3", *c.S3)
	}
	if c.GCS != nil {
		add("gcs", *c.GCS)
	}
	if c.Azure != nil {
		add("azure", *c.Azure)
	}
	switch len(names) {
	case 0:
		return "", nil, errors.New("exactly one driver must be configured: file, mem, s3, gcs, or azure")
	case 1:
		return names[0], selected, nil
	default:
		return "", nil, fmt.Errorf("exactly one driver must be configured, got %s", strings.Join(names, ", "))
	}
}

func (c Config) Validate() error {
	_, driver, err := c.Driver()
	if err != nil {
		return err
	}
	return driver.Validate()
}

// URL validates the selected driver and returns its bucket URL.
func (c Config) URL() (string, error) {
	_, driver, err := c.Driver()
	if err != nil {
		return "", err
	}
	if err := driver.Validate(); err != nil {
		return "", err
	}
	return driver.URL(), nil
}

// OpenConfig opens the bucket cfg selects.
func OpenConfig(ctx context.Context, cfg Config) (*Store, error) {
	bucketURL, err := cfg.URL()
	if err != nil {
		return nil, err
	}
	return Open(ctx, bucketURL)
}

// FileConfig stores blobs in a local directory, created with mode 0750.
// Temporary files are written inside it so commits are same-filesystem
// renames and the container root filesystem can stay read-only.
type FileConfig struct {
	Path string `json:"path"`
}

func (c FileConfig) Validate() error {
	if c.Path == "" {
		return errors.New("file.path must not be empty")
	}
	if !filepath.IsAbs(c.Path) {
		return fmt.Errorf("file.path %q must be absolute", c.Path)
	}
	return nil
}

func (c FileConfig) URL() string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Clean(c.Path))}
	u.RawQuery = url.Values{
		"create_dir":    {"true"},
		"dir_file_mode": {"488"}, // 0750
		"no_tmp_dir":    {"true"},
	}.Encode()
	return u.String()
}

// MemConfig keeps blobs in process memory, for tests.
type MemConfig struct{}

func (MemConfig) Validate() error { return nil }
func (MemConfig) URL() string     { return "mem://" }

// S3Config stores blobs in an S3 or S3-compatible bucket.
type S3Config struct {
	Bucket string `json:"bucket"`
	// Prefix is prepended to every object key; a trailing "/" is added.
	Prefix string `json:"prefix,omitempty"`
	Region string `json:"region,omitempty"`
	// Endpoint overrides the service URL, e.g. for MinIO.
	Endpoint     string `json:"endpoint,omitempty"`
	UsePathStyle bool   `json:"usePathStyle,omitempty"`
	DisableHTTPS bool   `json:"disableHTTPS,omitempty"`
	// Profile selects a shared-config profile.
	Profile   string `json:"profile,omitempty"`
	Anonymous bool   `json:"anonymous,omitempty"`
	// SSEType is AES256, aws:kms, or aws:kms:dsse; KMSKeyID needs a KMS type.
	SSEType  string `json:"sseType,omitempty"`
	KMSKeyID string `json:"kmsKeyID,omitempty"`
}

var s3BucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func (c S3Config) Validate() error {
	var errs []error
	if !s3BucketPattern.MatchString(c.Bucket) || strings.Contains(c.Bucket, "..") {
		errs = append(errs, fmt.Errorf("s3.bucket %q is not a valid bucket name", c.Bucket))
	}
	if err := validatePrefix("s3", c.Prefix); err != nil {
		errs = append(errs, err)
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("s3.endpoint %q must be an http or https URL", c.Endpoint))
		}
	}
	switch c.SSEType {
	case "", "AES256":
		if c.KMSKeyID != "" {
			errs = append(errs, errors.New("s3.kmsKeyID requires s3.sseType aws:kms or aws:kms:dsse"))
		}
	case "aws:kms", "aws:kms:dsse":
	default:
		errs = append(errs, fmt.Errorf("s3.sseType %q must be AES256, aws:kms, or aws:kms:dsse", c.SSEType))
	}
	return errors.Join(errs...)
}

func (c S3Config) URL() string {
	query := url.Values{}
	setString(query, "prefix", normalizePrefix(c.Prefix))
	setString(query, "region", c.Region)
	setString(query, "endpoint", c.Endpoint)
	setString(query, "profile", c.Profile)
	setBool(query, "use_path_style", c.UsePathStyle)
	setBool(query, "disable_https", c.DisableHTTPS)
	setBool(query, "anonymous", c.Anonymous)
	setString(query, "ssetype", c.SSEType)
	setString(query, "kmskeyid", c.KMSKeyID)
	return (&url.URL{Scheme: "s3", Host: c.Bucket, RawQuery: query.Encode()}).String()
}

// GCSConfig stores blobs in a Google Cloud Storage bucket.
type GCSConfig struct {
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	Anonymous bool   `json:"anonymous,omitempty"`
}

var gcsBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$`)

func (c GCSConfig) Validate() error {
	var errs []error
	if !gcsBucketPattern.MatchString(c.Bucket) {
		errs = append(errs, fmt.Errorf("gcs.bucket %q is not a valid bucket name", c.Bucket))
	}
	if err := validatePrefix("gcs", c.Prefix); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (c GCSConfig) URL() string {
	query := url.Values{}
	setString(query, "prefix", normalizePrefix(c.Prefix))
	setBool(query, "anonymous", c.Anonymous)
	return (&url.URL{Scheme: "gs", Host: c.Bucket, RawQuery: query.Encode()}).String()
}

// AzureConfig stores blobs in an Azure Blob Storage container. The account
// defaults to AZURE_STORAGE_ACCOUNT.
type AzureConfig struct {
	Container   string `json:"container"`
	Prefix      string `json:"prefix,omitempty"`
	AccountName string `json:"accountName,omitempty"`
	// Domain overrides the storage domain (default blob.core.windows.net).
	Domain string `json:"domain,omitempty"`
	// Protocol is https (default) or http.
	Protocol      string `json:"protocol,omitempty"`
	LocalEmulator bool   `json:"localEmulator,omitempty"`
}

var azureContainerPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9]|-[a-z0-9]){2,62}$`)

func (c AzureConfig) Validate() error {
	var errs []error
	if !azureContainerPattern.MatchString(c.Container) {
		errs = append(errs, fmt.Errorf("azure.container %q is not a valid container name", c.Container))
	}
	if err := validatePrefix("azure", c.Prefix); err != nil {
		errs = append(errs, err)
	}
	if c.Protocol != "" && c.Protocol != "http" && c.Protocol != "https" {
		errs = append(errs, fmt.Errorf("azure.protocol %q must be http or https", c.Protocol))
	}
	return errors.Join(errs...)
}

func (c AzureConfig) URL() string {
	query := url.Values{}
	setString(query, "prefix", normalizePrefix(c.Prefix))
	setString(query, "storage_account", c.AccountName)
	setString(query, "domain", c.Domain)
	setString(query, "protocol", c.Protocol)
	setBool(query, "localemu", c.LocalEmulator)
	return (&url.URL{Scheme: "azblob", Host: c.Container, RawQuery: query.Encode()}).String()
}

func validatePrefix(driver, prefix string) error {
	if strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "//") {
		return fmt.Errorf("%s.prefix %q must be relative and must not contain empty segments", driver, prefix)
	}
	return nil
}

func normalizePrefix(prefix string) string {
	if prefix == "" || strings.HasSuffix(prefix, "/") {
		return prefix
	}
	return prefix + "/"
}

func setString(query url.Values, key, value string) {
	if value != "" {
		query.Set(key, value)
	}
}

func setBool(query url.Values, key string, value bool) {
	if value {
		query.Set(key, "true")
	}
}
