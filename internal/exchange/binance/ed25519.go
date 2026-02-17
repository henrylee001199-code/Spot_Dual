package binance

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
)

func loadEd25519PrivateKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		return nil, errors.New("ws_ed25519_private_key_path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("empty ed25519 private key")
	}
	if block, _ := pem.Decode(data); block != nil {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		if k, ok := key.(ed25519.PrivateKey); ok {
			return k, nil
		}
		return nil, errors.New("unsupported private key type")
	}
	if raw, err := base64.StdEncoding.DecodeString(string(data)); err == nil {
		if len(raw) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(raw), nil
		}
	}
	if len(data) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(data), nil
	}
	return nil, errors.New("unsupported ed25519 private key format")
}
