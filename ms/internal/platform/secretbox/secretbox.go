// Package secretbox encrypts small secrets at rest (bank session ids) with
// AES-256-GCM and derives keyed hashes (one-time codes, OAuth state) from the
// same master key.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Box holds the derived keys. The zero value is unusable; use New.
type Box struct {
	aead   cipher.AEAD
	macKey []byte
}

// New builds a Box from a base64-encoded 32-byte master key.
func New(masterKeyB64 string) (*Box, error) {
	master, err := base64.StdEncoding.DecodeString(masterKeyB64)
	if err != nil {
		return nil, fmt.Errorf("secret key is not valid base64: %w", err)
	}
	if len(master) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes, got %d", len(master))
	}
	// Separate keys for encryption and MAC, derived with HMAC as a PRF.
	encKey := derive(master, "pff/encryption")
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, macKey: derive(master, "pff/mac")}, nil
}

func derive(master []byte, label string) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte(label))
	return m.Sum(nil)
}

// Seal encrypts plaintext; the output is nonce || ciphertext.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < ns {
		return nil, errors.New("secretbox: ciphertext too short")
	}
	return b.aead.Open(nil, sealed[:ns], sealed[ns:], nil)
}

// MAC returns HMAC-SHA256(key, parts...) with length-prefixed parts, so
// different splits of the same bytes never collide.
func (b *Box) MAC(parts ...[]byte) []byte {
	m := hmac.New(sha256.New, b.macKey)
	for _, p := range parts {
		var l [4]byte
		l[0], l[1], l[2], l[3] = byte(len(p)>>24), byte(len(p)>>16), byte(len(p)>>8), byte(len(p))
		m.Write(l[:])
		m.Write(p)
	}
	return m.Sum(nil)
}

// RandomToken returns n random bytes, URL-safe base64 encoded.
func RandomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
