package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// dekEntry holds the memoized result of one db's fetchOrCreateDEK call,
// guarded by its own sync.Once so that call runs at most once no matter how
// many goroutines race on it — mirroring keyring.go's process-wide
// kekOnce/kekCache/kekErr, but scoped per-DB since (unlike the KEK) the DEK
// is per-db, not process-wide.
type dekEntry struct {
	once sync.Once
	dek  []byte
	err  error
}

// dekEntries memoizes getOrCreateDEK per *sql.DB: unlike the KEK, a process
// may legitimately hold several distinct DBs open at once (e.g. a test suite
// opening multiple in-memory SQLite databases), each with its own DEK, so
// entries are keyed by the *sql.DB pointer rather than shared process-wide.
// Only the map lookup/insert itself is guarded by dekEntriesMu — a short
// critical section; the expensive fetchOrCreateDEK call runs inside that
// db's own dekEntry.once.Do, outside the mutex. Without this, two goroutines
// racing on the very first use of a given db (e.g. two workflow executions
// resolving @secret: refs concurrently) could both see sql.ErrNoRows on the
// SELECT and both attempt to INSERT the id=1 singleton vault_keys row; the
// loser's INSERT would fail and that call would return a spurious error
// instead of the real DEK.
var (
	dekEntriesMu sync.Mutex
	dekEntries   = map[*sql.DB]*dekEntry{}
)

// fetchDEK is the function getOrCreateDEK invokes to actually resolve the
// DEK on first use. It is a package-level variable (rather than a direct
// call to fetchOrCreateDEK) purely so tests can substitute a stub that
// fails on demand to exercise the retry-after-failure path below.
var fetchDEK = fetchOrCreateDEK

// getOrCreateDEK returns the unwrapped 32-byte Data Encryption Key, reading
// and unwrapping the singleton vault_keys row if present, or generating a
// new DEK (wrapped under the KEK from the OS keychain) and persisting it
// if this is the first use. A successful result is cached per db so
// repeated calls within a process skip the keychain/table round trip. A
// failed attempt is NOT cached: all callers racing on that one attempt
// observe the same error (via the shared sync.Once), but the next call
// after it completes gets a fresh attempt instead of being stuck forever
// with a stale transient error (e.g. a momentarily locked keychain or a
// SQLITE_BUSY on vault_keys).
func getOrCreateDEK(ctx context.Context, db *sql.DB) ([]byte, error) {
	dekEntriesMu.Lock()
	entry, ok := dekEntries[db]
	if !ok {
		entry = &dekEntry{}
		dekEntries[db] = entry
	}
	dekEntriesMu.Unlock()

	entry.once.Do(func() {
		entry.dek, entry.err = fetchDEK(ctx, db)
	})

	if entry.err != nil {
		dekEntriesMu.Lock()
		if dekEntries[db] == entry {
			delete(dekEntries, db)
		}
		dekEntriesMu.Unlock()
	}

	return entry.dek, entry.err
}

func fetchOrCreateDEK(ctx context.Context, db *sql.DB) ([]byte, error) {
	kek, err := getOrCreateKEK()
	if err != nil {
		return nil, fmt.Errorf("secrets: getOrCreateDEK: %w", err)
	}

	var wrappedDEK, wrappedNonce []byte
	err = db.QueryRowContext(ctx, `SELECT wrapped_dek, wrapped_nonce FROM vault_keys WHERE id = 1`).
		Scan(&wrappedDEK, &wrappedNonce)
	if err == sql.ErrNoRows {
		return createDEK(ctx, db, kek)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: reading vault_keys: %w", err)
	}

	dek, err := Decrypt(kek, wrappedDEK, wrappedNonce)
	if err != nil {
		return nil, fmt.Errorf("secrets: unwrapping DEK: %w", err)
	}
	return dek, nil
}

func createDEK(ctx context.Context, db *sql.DB, kek []byte) ([]byte, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("secrets: generating DEK: %w", err)
	}
	wrappedDEK, wrappedNonce, err := Encrypt(kek, dek)
	if err != nil {
		return nil, fmt.Errorf("secrets: wrapping DEK: %w", err)
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO vault_keys (id, wrapped_dek, wrapped_nonce, created_at) VALUES (1, ?, ?, ?)`,
		wrappedDEK, wrappedNonce, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("secrets: storing wrapped DEK: %w", err)
	}
	return dek, nil
}
