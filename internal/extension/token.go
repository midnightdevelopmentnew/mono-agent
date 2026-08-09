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

// tokenPath returns the path to the relay's shared-secret token file.
func tokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".monoagent", "extension.token"), nil
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
