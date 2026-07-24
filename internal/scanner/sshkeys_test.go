package scanner

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// writeTestKey writes an unencrypted PKCS#8 ed25519 private key (which
// ssh.ParsePrivateKey accepts) to dir/name and returns the path.
func writeTestKey(t *testing.T, dir, name string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

func TestResolveSSHFetchAuth_ExplicitFiles(t *testing.T) {
	dir := t.TempDir()
	key1 := writeTestKey(t, dir, "key1")
	key2 := writeTestKey(t, dir, "key2")
	missing := filepath.Join(dir, "does-not-exist")
	if err := os.WriteFile(filepath.Join(dir, "garbage"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(dir, "garbage")

	fa := ResolveSSHFetchAuth([]string{key1, missing, garbage, key2})
	if fa == nil {
		t.Fatal("expected FetchAuth, got nil")
	}
	// The two valid keys are kept; the missing and non-key files are skipped.
	if len(fa.SshKeys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(fa.SshKeys))
	}
	// Keys carry an empty host (offered as defaults) and the raw PEM bytes.
	for _, k := range fa.SshKeys {
		if k.Host != "" {
			t.Errorf("expected empty host, got %q", k.Host)
		}
		if len(k.PrivateKey) == 0 {
			t.Error("expected private key bytes")
		}
	}
}

func TestResolveSSHFetchAuth_NoUsableKeys(t *testing.T) {
	dir := t.TempDir()
	if fa := ResolveSSHFetchAuth([]string{filepath.Join(dir, "nope")}); fa != nil {
		t.Fatalf("expected nil when no usable key, got %#v", fa)
	}
}

func TestResolveSSHFetchAuth_DefaultScan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestKey(t, filepath.Join(home, ".ssh"), "id_ed25519")

	// Empty list -> scan the default ~/.ssh key files under the fake HOME.
	fa := ResolveSSHFetchAuth(nil)
	if fa == nil {
		t.Fatal("expected the default id_ed25519 to be picked up")
	}
	if len(fa.SshKeys) != 1 {
		t.Fatalf("expected 1 default key, got %d", len(fa.SshKeys))
	}
}
