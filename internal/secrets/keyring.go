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

// kekAttempt holds the memoized result of one getOrCreateKEK attempt,
// guarded by its own sync.Once so that attempt's fetchKEK call runs at most
// once no matter how many goroutines race on it. This is the same
// per-attempt struct pattern dek.go's dekEntry uses: each attempt is its own
// object, so a straggler goroutine reading a completed (and possibly
// already-superseded) attempt's fields can never race with a fresh attempt's
// writes — they're different objects in memory, not shared package
// variables.
type kekAttempt struct {
	once sync.Once
	kek  []byte
	err  error
}

// currentKEKAttempt memoizes getOrCreateKEK for the lifetime of the process:
// the KEK is process-wide and doesn't depend on any db parameter, so every
// call after the first can skip the OS keychain round trip (on macOS,
// keyring.Get forks a /usr/bin/security subprocess per call — expensive when
// called repeatedly, e.g. while migrating many connections). A fresh
// process (e.g. a new CLI invocation) still re-fetches once, which is
// correct.
//
// A failed attempt is NOT cached: kekMu only guards the currentKEKAttempt
// pointer itself (a short critical section), while the expensive fetchKEK
// call runs inside that attempt's own sync.Once.Do, outside the mutex, so
// all callers racing on one in-flight attempt observe the identical shared
// result. If that attempt fails, the pointer is swapped for a fresh
// *kekAttempt so the next call gets a real retry instead of being stuck
// forever with a stale transient error (e.g. a momentarily locked
// keychain) — mirroring the retry-after-failure behavior dek.go's per-db
// getOrCreateDEK uses.
var (
	kekMu             sync.Mutex
	currentKEKAttempt = &kekAttempt{}
)

// keyringIOMu serializes every raw keyring.Get/Set call this package makes.
// It exists for a narrower reason than kekMu/currentKEKAttempt above (which
// only dedupes concurrent callers within one already-known-outcome
// attempt): peekKEK (dek.go's lockless fast path) and fetchOrCreateKEK
// (which dek.go's bootstrapDEKLocked calls while holding a cross-process DB
// lock that says nothing about the keychain) can otherwise run truly
// concurrently — e.g. one goroutine's fast-path Get racing another
// goroutine's in-progress Set of a brand-new KEK. Real OS keychain backends
// (macOS's /usr/bin/security subprocess, Linux Secret Service over D-Bus,
// Windows Credential Manager) are out-of-process and handle concurrent
// access safely on their own, so this isn't a corruption risk there — but
// go-keyring's in-memory mock (used in tests) has no such protection, and a
// process that legitimately holds multiple *sql.DB open at once (see
// dekEntries below) genuinely can reach the keychain from two goroutines at
// once for the same process-wide KEK. This mutex makes this package safe
// either way instead of relying on the backend's own guarantees.
var keyringIOMu sync.Mutex

// fetchKEK is the function getOrCreateKEK invokes to actually resolve the
// KEK on first use. It is a package-level variable (rather than a direct
// call to fetchOrCreateKEK) purely so tests can substitute a stub that
// fails on demand to exercise the retry-after-failure path below.
var fetchKEK = fetchOrCreateKEK

// getOrCreateKEK returns the 32-byte Key Encryption Key stored in the OS
// keychain (macOS Keychain / Linux Secret Service / Windows Credential
// Manager, via zalando/go-keyring — no cgo), generating and storing a new
// one on first use. The KEK never touches disk; only the DEK it wraps does
// (see dek.go).
func getOrCreateKEK() ([]byte, error) {
	kekMu.Lock()
	attempt := currentKEKAttempt
	kekMu.Unlock()

	attempt.once.Do(func() {
		attempt.kek, attempt.err = fetchKEK()
	})

	if attempt.err != nil {
		kekMu.Lock()
		if currentKEKAttempt == attempt {
			currentKEKAttempt = &kekAttempt{}
		}
		kekMu.Unlock()
	}

	return attempt.kek, attempt.err
}

// peekKEK reads the KEK from the OS keychain without creating one if it's
// missing — a pure read used by fetchOrCreateDEK's fast path (dek.go) to
// decide whether cross-process bootstrap locking is needed at all. Bypasses
// the in-process memoization layer, same as fetchOrCreateKEK.
func peekKEK() (kek []byte, found bool, err error) {
	keyringIOMu.Lock()
	defer keyringIOMu.Unlock()

	stored, err := keyring.Get(keyringService, keyringAccount)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("secrets: reading KEK from keychain: %w", err)
	}
	key, decodeErr := hex.DecodeString(stored)
	if decodeErr != nil {
		return nil, false, fmt.Errorf("secrets: decoding stored KEK: %w", decodeErr)
	}
	return key, true, nil
}

func fetchOrCreateKEK() ([]byte, error) {
	keyringIOMu.Lock()
	defer keyringIOMu.Unlock()

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
