package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestGenerate(t *testing.T) {
	dir := t.TempDir()
	if err := generate(dir); err != nil {
		t.Fatal(err)
	}
	password, err := os.ReadFile(filepath.Join(dir, "registry-password"))
	if err != nil || len(password) == 0 {
		t.Fatalf("password: %q, %v", password, err)
	}
	auth, err := os.ReadFile(filepath.Join(dir, "htpasswd"))
	if err != nil || !strings.HasPrefix(string(auth), "e2e:") {
		t.Fatalf("htpasswd: %q, %v", auth, err)
	}
	hash := strings.TrimSpace(strings.TrimPrefix(string(auth), "e2e:"))
	if err := bcrypt.CompareHashAndPassword([]byte(hash), password); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"htpasswd", "registry-password"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions = %v", name, info.Mode())
		}
	}
	if err := generate(dir); err != nil {
		t.Fatal(err)
	}
	next, err := os.ReadFile(filepath.Join(dir, "registry-password"))
	if err != nil || string(next) == string(password) {
		t.Fatalf("credentials were not randomized: %v", err)
	}
}
