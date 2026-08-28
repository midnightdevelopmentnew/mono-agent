package extension

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// tokenHeader carries the shared-secret token that authenticates relay
// requests (see handleRelay). Only local monoagentcli processes that can
// read ~/.monoagent/extension.token can drive the extension through it —
// an arbitrary local process or web page cannot.
const tokenHeader = "X-Monoagent-Extension-Token"

// tokenProfile scopes the token file to a profile, mirroring the per-profile
// bridge ports in cmd/monoagentcli. Set once at startup via SetTokenProfile.
var tokenProfile string

// SetTokenProfile scopes this process's token file to profileID. Ports are
// already per-profile, so two profiles can each legitimately win a bind; with
// a single global token file the second to start overwrote the first's secret
// and every relay call on the first returned "unauthorized". Observed
// 2026-08-22: starting a linkedin-management bridge silently killed the X
// automation running on the default profile.
func SetTokenProfile(profileID string) {
	tokenProfile = profileID
}

// tokenPath returns the path to the relay's shared-secret token file. The
// default profile keeps the unsuffixed name so existing installs still work.
func tokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	name := "extension.token"
	if tokenProfile != "" && tokenProfile != "default" {
		name = "extension." + tokenProfile + ".token"
	}
	return filepath.Join(home, ".monoagent", name), nil
}

// generateToken creates a new random token and writes it to tokenPath,
// readable only by the current user. Call this only after winning the
// extension server's port bind: since exactly one process can ever hold
// that port, there is no write race with other processes that lose the
// bind and relay through the winner instead.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(buf)

	path, err := tokenPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return token, nil
}

// loadToken reads the token written by generateToken. Callers that relay
// through an already-running server (rather than owning it) use this, since
// the server holding the port bind is the source of truth for the token.
func loadToken() (string, error) {
	path, err := tokenPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return string(data), nil
}
