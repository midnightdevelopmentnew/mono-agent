package secrets

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
)

const secretRefPrefix = "@secret:"

// Resolve turns "@secret:<name>" into the decrypted plaintext value stored
// in the vault under profileID. Strings that don't carry the "@secret:"
// prefix are returned unchanged. Returns an error if the reference is
// present but no matching entry is found.
func Resolve(ctx context.Context, db *sql.DB, profileID, ref string) (string, error) {
	if !strings.HasPrefix(ref, secretRefPrefix) {
		return ref, nil
	}
	name := strings.TrimPrefix(ref, secretRefPrefix)

	entries, err := List(ctx, db, profileID)
	if err != nil {
		return ref, fmt.Errorf("secrets.Resolve: %w", err)
	}
	var id string
	for _, e := range entries {
		if e.Name == name {
			id = e.ID
			break
		}
	}
	if id == "" {
		return ref, fmt.Errorf("secrets.Resolve: secret %q not found", name)
	}

	value, err := DecryptEntry(ctx, db, profileID, id)
	if err != nil {
		return ref, fmt.Errorf("secrets.Resolve: %w", err)
	}
	return value, nil
}

// ResolveConfig walks a config map and replaces any string value that starts
// with "@secret:" with its decrypted value from the vault. Mirrors
// vault.ResolveConfig's behavior: a missing/not-found secret is non-fatal —
// the original "@secret:" string is left in place and a warning is logged to
// stderr, rather than failing the whole config resolution. This is a
// deliberate difference from Resolve/DecryptEntry's own not-found behavior
// (which errors), because at this integration point matching the
// established @img- convention is what's consistent with the surrounding
// code (see internal/workflow/execution.go, right after its
// vault.ResolveConfig call).
func ResolveConfig(ctx context.Context, db *sql.DB, profileID string, config map[string]interface{}) error {
	if db == nil {
		return nil
	}
	for k, v := range config {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if !strings.HasPrefix(s, secretRefPrefix) {
			continue
		}
		resolved, err := Resolve(ctx, db, profileID, s)
		if err != nil {
			// Non-fatal: leave original ref, emit warning.
			fmt.Fprintf(os.Stderr, "secrets: warning: %v\n", err)
			continue
		}
		config[k] = resolved
	}
	return nil
}
