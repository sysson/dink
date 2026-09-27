package blobstore_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sysson/dink/core/registry/backend/blobstore"
)

func TestConfigURL(t *testing.T) {
	tests := map[string]struct {
		cfg  blobstore.Config
		want string
	}{
		"file": {
			cfg:  blobstore.Config{File: &blobstore.FileConfig{Path: "/var/lib/dinki/blobs/"}},
			want: "file:///var/lib/dinki/blobs?create_dir=true&dir_file_mode=488&no_tmp_dir=true",
		},
		"mem": {cfg: blobstore.Config{Mem: &blobstore.MemConfig{}}, want: "mem://"},
		"s3": {
			cfg: blobstore.Config{S3: &blobstore.S3Config{
				Bucket: "dinki", Prefix: "registry", Region: "eu-west-1",
				Endpoint: "http://minio:9000", UsePathStyle: true, DisableHTTPS: true,
				SSEType: "aws:kms", KMSKeyID: "key",
			}},
			want: "s3://dinki?disable_https=true&endpoint=http%3A%2F%2Fminio%3A9000&kmskeyid=key&prefix=registry%2F&region=eu-west-1&ssetype=aws%3Akms&use_path_style=true",
		},
		"gcs": {
			cfg:  blobstore.Config{GCS: &blobstore.GCSConfig{Bucket: "dinki-blobs", Prefix: "a/b/"}},
			want: "gs://dinki-blobs?prefix=a%2Fb%2F",
		},
		"azure": {
			cfg:  blobstore.Config{Azure: &blobstore.AzureConfig{Container: "dinki", AccountName: "acct", Protocol: "https"}},
			want: "azblob://dinki?protocol=https&storage_account=acct",
		},
	}
	for name, test := range tests {
		got, err := test.cfg.URL()
		if err != nil {
			t.Fatalf("%s: URL() error = %v", name, err)
		}
		if got != test.want {
			t.Errorf("%s: URL() = %s, want %s", name, got, test.want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	invalid := map[string]blobstore.Config{
		"none":            {},
		"two":             {File: &blobstore.FileConfig{Path: "/a"}, Mem: &blobstore.MemConfig{}},
		"relative file":   {File: &blobstore.FileConfig{Path: "blobs"}},
		"empty file":      {File: &blobstore.FileConfig{}},
		"s3 bucket":       {S3: &blobstore.S3Config{Bucket: "Bad_Bucket"}},
		"s3 endpoint":     {S3: &blobstore.S3Config{Bucket: "dinki", Endpoint: "minio:9000"}},
		"s3 sse":          {S3: &blobstore.S3Config{Bucket: "dinki", SSEType: "rot13"}},
		"s3 kms no sse":   {S3: &blobstore.S3Config{Bucket: "dinki", KMSKeyID: "k"}},
		"s3 prefix":       {S3: &blobstore.S3Config{Bucket: "dinki", Prefix: "/abs"}},
		"gcs bucket":      {GCS: &blobstore.GCSConfig{}},
		"azure container": {Azure: &blobstore.AzureConfig{Container: "Bad--name"}},
		"azure protocol":  {Azure: &blobstore.AzureConfig{Container: "dinki", Protocol: "ftp"}},
	}
	for name, cfg := range invalid {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
}

func TestConfigUnmarshalReplaces(t *testing.T) {
	cfg := blobstore.Config{File: &blobstore.FileConfig{Path: "/default"}}
	if err := json.Unmarshal([]byte(`{"s3":{"bucket":"dinki"}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.File != nil || cfg.S3 == nil || cfg.S3.Bucket != "dinki" {
		t.Fatalf("Unmarshal merged instead of replacing: %+v", cfg)
	}
	if err := json.Unmarshal([]byte(`{"s4":{}}`), &cfg); err == nil || !strings.Contains(err.Error(), "s4") {
		t.Fatalf("unknown driver error = %v", err)
	}
	if err := json.Unmarshal([]byte(`{"s3":{"bucket":"dinki","bukket":"x"}}`), &cfg); err == nil {
		t.Fatal("unknown driver field accepted")
	}
}

// Opening a bucket does not contact the service, so this checks that Go CDK
// accepts every parameter the configs generate.
func TestOpenConfigAcceptsGeneratedURLs(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AZURE_STORAGE_KEY", "dGVzdA==")
	configs := map[string]blobstore.Config{
		"file": {File: &blobstore.FileConfig{Path: filepath.Join(t.TempDir(), "blobs")}},
		"mem":  {Mem: &blobstore.MemConfig{}},
		"s3": {S3: &blobstore.S3Config{
			Bucket: "dinki", Prefix: "p", Region: "eu-west-1", Endpoint: "http://127.0.0.1:1",
			UsePathStyle: true, DisableHTTPS: true, SSEType: "aws:kms", KMSKeyID: "k",
		}},
		"gcs":   {GCS: &blobstore.GCSConfig{Bucket: "dinki", Prefix: "p", Anonymous: true}},
		"azure": {Azure: &blobstore.AzureConfig{Container: "dinki", AccountName: "acct", Protocol: "http", Domain: "127.0.0.1:1", Prefix: "p"}},
	}
	for name, cfg := range configs {
		store, err := blobstore.OpenConfig(context.Background(), cfg)
		if err != nil {
			t.Errorf("%s: OpenConfig() error = %v", name, err)
			continue
		}
		_ = store.Close()
	}
}
