package types

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// PullOptions configures an image pull.
type ImagePullOptions struct {
	Auth        *RegistryAuth
	MetaHeaders map[string][]string
	OutStream   io.Writer
	Platforms   []ocispec.Platform
}

type ImageListOptions struct {
	All        bool
	Filters    Args
	SharedSize bool
	Manifests  bool
	Identity   bool
}

// RegistryAuth holds registry credentials supplied by a Docker-compatible API client.
type RegistryAuth struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	RefreshToken  string `json:"identitytoken,omitempty"`
	AccessToken   string `json:"registrytoken,omitempty"`
	ServerAddress string `json:"serveraddress,omitempty"`
}

// DecodeRegistryAuthHeader decodes the base64url-encoded JSON value from X-Registry-Auth.
func DecodeRegistryAuthHeader(authEncoded string) (*RegistryAuth, error) {
	if authEncoded == "" {
		return &RegistryAuth{}, nil
	}

	decoded, err := base64.URLEncoding.DecodeString(authEncoded)
	if err != nil {
		return &RegistryAuth{}, fmt.Errorf("invalid X-Registry-Auth header: must be a valid base64url-encoded string")
	}
	if bytes.Equal(decoded, []byte("{}")) {
		return &RegistryAuth{}, nil
	}

	auth := &RegistryAuth{}
	if err := json.Unmarshal(decoded, auth); err != nil {
		return &RegistryAuth{}, fmt.Errorf("invalid X-Registry-Auth header: invalid JSON: %w", err)
	}
	return auth, nil
}
