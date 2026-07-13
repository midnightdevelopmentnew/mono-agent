package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "monoagent-vault"
	keyringAccount = "kek"
)

// getOrCreateKEK returns the 32-byte Key Encryption Key stored in the OS
// keychain (macOS Keychain / Linux Secret Service / Windows Credential
// Manager, via zalando/go-keyring — no cgo), generating and storing a new
// one on first use. The KEK never touches disk; only the DEK it wraps does
// (see dek.go).
func getOrCreateKEK() ([]byte, error) {
	stored, err := keyring.Get(keyringService, keyringAccount)
	if err == nil {
		key, decodeErr := hex.DecodeString(stored)
		if decodeErr != nil {
			return nil, fmt.Errorf("secrets: decoding stored KEK: %w", decodeErr)
		}
		return key, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("secrets: reading KEK from keychain: %w", err)
	}

	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		return nil, fmt.Errorf("secrets: generating KEK: %w", err)
	}
	if err := keyring.Set(keyringService, keyringAccount, hex.EncodeToString(kek)); err != nil {
		return nil, fmt.Errorf("secrets: storing KEK in keychain: %w", err)
	}
	return kek, nil
}
