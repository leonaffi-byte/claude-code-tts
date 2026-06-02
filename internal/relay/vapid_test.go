package relay

import (
	"os"
	"strings"
	"testing"
)

func TestVAPIDStore_LoadOrGenerate_PrivateKeyFilePermissions_Are0600(t *testing.T) {
	dir := t.TempDir()
	vs := NewVAPIDStore(dir)

	_, _, err := vs.LoadOrGenerate()
	if err != nil {
		t.Fatalf("LoadOrGenerate failed: %v", err)
	}

	info, err := os.Stat(vs.privPath)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("private key file permissions = %04o, want 0600", perm)
	}
}

func TestVAPIDStore_LoadOrGenerate_ReturnsNonEmptyKeys(t *testing.T) {
	dir := t.TempDir()
	vs := NewVAPIDStore(dir)

	priv, pub, err := vs.LoadOrGenerate()
	if err != nil {
		t.Fatalf("LoadOrGenerate failed: %v", err)
	}
	if priv == "" {
		t.Error("LoadOrGenerate returned empty private key")
	}
	if pub == "" {
		t.Error("LoadOrGenerate returned empty public key")
	}
}

func TestVAPIDStore_InMemoryGetters_MatchReturnedKeys(t *testing.T) {
	dir := t.TempDir()
	vs := NewVAPIDStore(dir)

	priv, pub, err := vs.LoadOrGenerate()
	if err != nil {
		t.Fatalf("LoadOrGenerate failed: %v", err)
	}

	if vs.PublicKey() != pub {
		t.Errorf("PublicKey() = %q, want %q", vs.PublicKey(), pub)
	}
	if vs.PrivateKey() != priv {
		t.Errorf("PrivateKey() = %q, want %q", vs.PrivateKey(), priv)
	}
}

func TestVAPIDStore_PublicKey_IsURLSafeBase64(t *testing.T) {
	dir := t.TempDir()
	vs := NewVAPIDStore(dir)

	_, pub, err := vs.LoadOrGenerate()
	if err != nil {
		t.Fatalf("LoadOrGenerate failed: %v", err)
	}

	// URL-safe base64 must not contain '+' or '/' or padding '='.
	if strings.ContainsAny(pub, "+/=") {
		t.Errorf("public key %q is not URL-safe base64 (contains +, / or =)", pub)
	}
}
