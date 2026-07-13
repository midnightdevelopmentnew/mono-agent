package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "monoagent-vault"
	keyringAccount = "kek"
)

// kekOnce/kekCache memoize getOrCreateKEK for the lifetime of the process:
// the KEK is process-wide and doesn't depend on any db parameter, so every
// call after the first can skip the OS keychain round trip (on macOS,
// keyring.Get forks a /usr/bin/security subprocess per call — expensive when
// called repeatedly, e.g. while migrating many connections). A fresh
// process (e.g. a new CLI invocation) still re-fetches once, which is
// correct.
var (
	kekOnce  sync.Once
	kekCache []byte
	kekErr   error
)

// getOrCreateKEK returns the 32-byte Key Encryption Key stored in the OS
// keychain (macOS Keychain / Linux Secret Service / Windows Credential
// Manager, via zalando/go-keyring — no cgo), generating and storing a new
// one on first use. The KEK never touches disk; only the DEK it wraps does
// (see dek.go).
func getOrCreateKEK() ([]byte, error) {
	kekOnce.Do(func() {
		kekCache, kekErr = fetchOrCreateKEK()
	})
	return kekCache, kekErr
}

func fetchOrCreateKEK() ([]byte, error) {
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
