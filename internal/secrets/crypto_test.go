package secrets

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	plaintext := []byte("super secret value")

	ciphertext, nonce, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext must not equal plaintext")
	}

	got, err := Decrypt(key, ciphertext, nonce)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestDecrypt_WrongKeyFails(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	wrongKey := make([]byte, 32)
	rand.Read(wrongKey)

	ciphertext, nonce, err := Encrypt(key, []byte("data"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(wrongKey, ciphertext, nonce); err == nil {
		t.Fatal("expected error decrypting with wrong key, got nil")
	}
}

func TestEncrypt_NonceUniquePerCall(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	_, nonce1, _ := Encrypt(key, []byte("a"))
	_, nonce2, _ := Encrypt(key, []byte("a"))
	if bytes.Equal(nonce1, nonce2) {
		t.Fatal("nonces must differ across calls")
	}
}
