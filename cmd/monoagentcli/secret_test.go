package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"monoagent/internal/connections"
	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newSecretCLITestDB(t *testing.T) string {
	t.Helper()
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "cli-secret-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.DB.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

func runSecretCmd(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: true}
	cmd := newSecretCmd(cfg)
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestSecretAddListGetReveal(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	addOut, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "openai-key", "--value", "sk-test123")
	if err != nil {
		t.Fatalf("secret add: %v (%s)", err, addOut)
	}

	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	if !strings.Contains(listOut, "openai-key") {
		t.Fatalf("expected list output to contain entry name, got: %s", listOut)
	}
	if strings.Contains(listOut, "sk-test123") {
		t.Fatal("secret list must never contain the plaintext value")
	}

	getOut, err := runSecretCmd(t, dbPath, "get", "openai-key")
	if err != nil {
		t.Fatalf("secret get: %v", err)
	}
	if strings.Contains(getOut, "sk-test123") {
		t.Fatal("secret get must never return the plaintext value")
	}

	revealOut, err := runSecretCmd(t, dbPath, "reveal", "openai-key", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if !strings.Contains(revealOut, "sk-test123") {
		t.Fatalf("expected reveal output to contain plaintext, got: %s", revealOut)
	}
}

// TestSecretAdd_ReadsValueFromStdinWhenFlagOmitted covers the fallback path
// in newSecretAddCmd that reads the secret value from stdin (via
// bufio.NewReader(os.Stdin).ReadString('\n') + strings.TrimRight) when
// --value is not passed, so real interactive use never needs to put a
// secret on the command line. It redirects os.Stdin for the duration of the
// `add` call, matching the os.Pipe idiom used by captureStdout in
// people_status_test.go, then round-trips the value through `reveal` to
// prove it was read and trimmed correctly (not just "did not error").
func TestSecretAdd_ReadsValueFromStdinWhenFlagOmitted(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()

	go func() {
		io.WriteString(w, "stdin-secret-value\n")
		w.Close()
	}()

	addOut, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "stdin-key")
	os.Stdin = orig
	if err != nil {
		t.Fatalf("secret add: %v (%s)", err, addOut)
	}

	revealOut, err := runSecretCmd(t, dbPath, "reveal", "stdin-key", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if !strings.Contains(revealOut, "stdin-secret-value") {
		t.Fatalf("expected reveal output to contain the value read from stdin, got: %s", revealOut)
	}
	if strings.Contains(revealOut, "stdin-secret-value\n\n") {
		t.Fatalf("stdin value was not trimmed correctly, got: %q", revealOut)
	}
}

func TestSecretAdd_RejectsInvalidKind(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	_, err := runSecretCmd(t, dbPath, "add", "--kind", "bogus", "--name", "x", "--value", "y")
	if err == nil {
		t.Fatal("expected error for invalid --kind, got nil")
	}
	if !strings.Contains(err.Error(), "invalid kind") {
		t.Fatalf("expected error to mention invalid kind, got: %v", err)
	}

	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(listOut), &entries); err != nil {
		t.Fatalf("unmarshal list output: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entry to be created for invalid kind, got %d", len(entries))
	}
}

func TestSecretReveal_RequiresConfirmationFlag(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	if _, err := runSecretCmd(t, dbPath, "reveal", "x"); err == nil {
		t.Fatal("expected error when --reveal flag is omitted")
	}
}

func TestSecretEncryptConnections_MigratesPlaintextRow(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	// The connections table isn't part of the SQL migrations (Store.EnsureTable
	// creates it lazily on first use by `connect`/the GUI); ensure it exists
	// here so the raw INSERT below has somewhere to land.
	if err := connections.NewStore(db.DB).EnsureTable(context.Background()); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	// Insert a connection row the old way: raw plaintext JSON in `data`,
	// bypassing Store.Save so this test can be sure it starts unencrypted.
	_, err = db.DB.Exec(`
		INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, created_at, updated_at)
		VALUES ('conn-1', 'x', 'oauth', 'Test', '', '{"access_token":"plaintext-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seeding plaintext connection: %v", err)
	}
	db.DB.Close()

	out, err := runSecretCmd(t, dbPath, "encrypt-connections")
	if err != nil {
		t.Fatalf("secret encrypt-connections: %v (%s)", err, out)
	}

	db2, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db2.DB.Close()
	var rawData string
	if err := db2.DB.QueryRow(`SELECT data FROM connections WHERE id = 'conn-1'`).Scan(&rawData); err != nil {
		t.Fatalf("reading migrated row: %v", err)
	}
	if strings.Contains(rawData, "plaintext-token") {
		t.Fatal("connections.data must not contain plaintext after encrypt-connections")
	}
	if !strings.HasPrefix(rawData, "vaultenc:v1:") {
		t.Fatalf("expected vaultenc-prefixed ciphertext, got: %s", rawData)
	}
}

func TestSecretRm_DeletesEntry(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "temp", "--value", "v"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	if _, err := runSecretCmd(t, dbPath, "rm", "temp"); err != nil {
		t.Fatalf("secret rm: %v", err)
	}
	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(listOut), &entries); err != nil {
		t.Fatalf("unmarshal list output: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after rm, got %d", len(entries))
	}
}
