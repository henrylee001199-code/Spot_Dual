package binance

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEd25519PrivateKeyBase64(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "ed25519.key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := loadEd25519PrivateKey(path)
	if err != nil {
		t.Fatalf("loadEd25519PrivateKey() error = %v", err)
	}
	if string(got) != string(priv) {
		t.Fatalf("loaded private key mismatch")
	}
}

func TestLoadEd25519PrivateKeyInvalidFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(path, []byte("not-a-key"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := loadEd25519PrivateKey(path); err == nil {
		t.Fatalf("loadEd25519PrivateKey() error = nil, want non-nil")
	}
}
