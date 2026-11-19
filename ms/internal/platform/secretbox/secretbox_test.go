package secretbox

import (
	"bytes"
	"crypto/hmac"
	"encoding/base64"
	"testing"
)

var key = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

func TestSealOpen(t *testing.T) {
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal([]byte("session-123"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("session-123")) {
		t.Fatal("plaintext visible in ciphertext")
	}
	got, err := b.Open(sealed)
	if err != nil || string(got) != "session-123" {
		t.Fatalf("open = %q, %v", got, err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := b.Open(sealed); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestMACIsLengthPrefixed(t *testing.T) {
	b, _ := New(key)
	if hmac.Equal(b.MAC([]byte("ab"), []byte("c")), b.MAC([]byte("a"), []byte("bc"))) {
		t.Fatal("MAC collides across part boundaries")
	}
}

func TestNewRejectsBadKeys(t *testing.T) {
	for _, k := range []string{"", "not-base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := New(k); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
}
