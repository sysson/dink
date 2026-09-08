package types

import (
	"encoding/base64"
	"testing"
)

func TestDecodeRegistryAuthHeader(t *testing.T) {
	encoded := base64.URLEncoding.EncodeToString([]byte(`{"username":"user","password":"pass","identitytoken":"refresh","registrytoken":"access"}`))

	auth, err := DecodeRegistryAuthHeader(encoded)
	if err != nil {
		t.Fatalf("DecodeRegistryAuthHeader: %v", err)
	}

	if auth.Username != "user" {
		t.Fatalf("Username = %q, want user", auth.Username)
	}
	if auth.Password != "pass" {
		t.Fatalf("Password = %q, want pass", auth.Password)
	}
	if auth.RefreshToken != "refresh" {
		t.Fatalf("RefreshToken = %q, want refresh", auth.RefreshToken)
	}
	if auth.AccessToken != "access" {
		t.Fatalf("AccessToken = %q, want access", auth.AccessToken)
	}
}

func TestDecodeRegistryAuthHeaderEmpty(t *testing.T) {
	auth, err := DecodeRegistryAuthHeader("")
	if err != nil {
		t.Fatalf("DecodeRegistryAuthHeader: %v", err)
	}
	if auth == nil {
		t.Fatal("expected an empty auth config")
	}
	if *auth != (RegistryAuth{}) {
		t.Fatalf("auth = %#v, want empty", auth)
	}
}

func TestDecodeRegistryAuthHeaderInvalidBase64(t *testing.T) {
	if _, err := DecodeRegistryAuthHeader("not base64"); err == nil {
		t.Fatal("expected invalid base64 to fail")
	}
}
