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
// many goroutines race on it. This is the same retry-on-failure/
// share-in-flight-result pattern keyring.go's getOrCreateKEK uses, scoped
// per-DB since (unlike the KEK) the DEK is per-db, not process-wide.
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

// fetchOrCreateDEK's fast path (KEK and wrapped DEK both already exist) is
// pure reads and needs no lock — the common case after first bootstrap. The
// slow path (either is still missing) hands off to bootstrapDEKLocked,
// which serializes the *entire* bootstrap — keychain and vault_keys
// together — across every process sharing db.
//
// Before this, getOrCreateKEK and the SELECT-then-INSERT on vault_keys each
// raced independently with no cross-process lock at all: two processes
// bootstrapping the same vault for the first time concurrently could each
// generate a different KEK, each call keyring.Set with no compare-and-swap
// (last write silently wins), and each attempt to INSERT the id=1 singleton
// DEK row wrapped under whichever KEK *they* generated (only one INSERT
// actually wins). Nothing errors at the moment of that race — the process
// whose own INSERT won stays internally self-consistent (via its own
// in-process memoization) for the rest of its lifetime — but the keychain
// may now hold the *other* process's KEK, wrapping nothing. The mismatch
// only surfaces later, on a different process (or that same process's own
// next launch), as an unrecoverable "cipher: message authentication
// failed" — silently destroying every stored secret except from an Export
// backup.
func fetchOrCreateDEK(ctx context.Context, db *sql.DB) ([]byte, error) {
	kek, kekFound, err := peekKEK()
	if err != nil {
		return nil, fmt.Errorf("secrets: getOrCreateDEK: %w", err)
	}
	if kekFound {
		var wrappedDEK, wrappedNonce []byte
		err := db.QueryRowContext(ctx, `SELECT wrapped_dek, wrapped_nonce FROM vault_keys WHERE id = 1`).
			Scan(&wrappedDEK, &wrappedNonce)
		switch {
		case err == nil:
			dek, err := Decrypt(kek, wrappedDEK, wrappedNonce)
			if err != nil {
				return nil, fmt.Errorf("secrets: unwrapping DEK: %w", err)
			}
			return dek, nil
		case err != sql.ErrNoRows:
			return nil, fmt.Errorf("secrets: reading vault_keys: %w", err)
		}
	}

	return bootstrapDEKLocked(ctx, db)
}

// bootstrapDEKLocked serializes first-time KEK/DEK creation across every
// process sharing db, reusing the same BEGIN IMMEDIATE pattern
// vault.Register (internal/vault/vault.go) and secrets.addEntry use for
// cross-process singleton allocation — see either for why BEGIN IMMEDIATE
// specifically is required (it acquires SQLite's write lock up front,
// unlike the default DEFERRED transaction). Non-DB work (the keychain round
// trip) running inside the transaction has the same precedent: vault.Register
// does its file copy inside its own BEGIN IMMEDIATE for the identical reason.
//
// Re-checks both the keychain and vault_keys after acquiring the lock,
// since another process may have completed bootstrap in the window between
// fetchOrCreateDEK's fast-path peek and this function acquiring the lock.
func bootstrapDEKLocked(ctx context.Context, db *sql.DB) ([]byte, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("secrets: bootstrapDEKLocked: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("secrets: bootstrapDEKLocked: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// fetchOrCreateKEK (not the memoized getOrCreateKEK): we're already
	// holding a cross-process lock stronger than its in-process sync.Once.
	kek, err := fetchOrCreateKEK()
	if err != nil {
		return nil, fmt.Errorf("secrets: bootstrapDEKLocked: %w", err)
	}

	var wrappedDEK, wrappedNonce []byte
	err = conn.QueryRowContext(ctx, `SELECT wrapped_dek, wrapped_nonce FROM vault_keys WHERE id = 1`).
		Scan(&wrappedDEK, &wrappedNonce)

	var dek []byte
	switch {
	case err == sql.ErrNoRows:
		dek, err = createDEK(ctx, conn, kek)
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, fmt.Errorf("secrets: reading vault_keys: %w", err)
	default:
		// Another process already finished bootstrapping between our
		// fast-path peek and acquiring this lock.
		dek, err = Decrypt(kek, wrappedDEK, wrappedNonce)
		if err != nil {
			return nil, fmt.Errorf("secrets: unwrapping DEK: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("secrets: bootstrapDEKLocked: commit: %w", err)
	}
	committed = true
	return dek, nil
}

// dbExecer is the subset of *sql.DB / *sql.Conn createDEK needs — it's
// always called with the *sql.Conn already holding bootstrapDEKLocked's
// BEGIN IMMEDIATE transaction.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

func createDEK(ctx context.Context, execer dbExecer, kek []byte) ([]byte, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("secrets: generating DEK: %w", err)
	}
	wrappedDEK, wrappedNonce, err := Encrypt(kek, dek)
	if err != nil {
		return nil, fmt.Errorf("secrets: wrapping DEK: %w", err)
	}
	_, err = execer.ExecContext(ctx,
		`INSERT INTO vault_keys (id, wrapped_dek, wrapped_nonce, created_at) VALUES (1, ?, ?, ?)`,
		wrappedDEK, wrappedNonce, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("secrets: storing wrapped DEK: %w", err)
	}
	return dek, nil
}
