package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"monoagent/internal/monomind"
)

// writeFakeIncompatibleMonomind mimics a pre-protocol monomind install: it
// answers unknown subcommands (including `--version --json`) with
// human-readable help text instead of JSON, exit 0 — the real
// installed-but-too-old case.
func writeFakeIncompatibleMonomind(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "monomind")
	script := "#!/bin/sh\necho 'Agent Management Commands'\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake monomind: %v", err)
	}
	return path
}

// TestGenerateConfigFailsFastOnIncompatibleMonomind guards against a
// regression where GenerateConfig resolved a runtime via Find()+Scan()
// without ever handshaking: against a too-old/incompatible monomind it
// would previously return a silent, useless success instead of the
// actionable "cache-only mode" error the Tier-3 fallback story depends on.
func TestGenerateConfigFailsFastOnIncompatibleMonomind(t *testing.T) {
	t.Setenv(monomind.EnvOverride, writeFakeIncompatibleMonomind(t))

	g := NewAgentGenerator(zerolog.Nop())
	_, err := g.GenerateConfig(context.Background(), "test-config", "<html></html>", "extract title", nil)
	if err == nil {
		t.Fatal("GenerateConfig() = nil error, want a cache-only/handshake error against an incompatible monomind")
	}
	if !strings.Contains(err.Error(), "cache-only mode") {
		t.Errorf("GenerateConfig() error = %q, want it to mention cache-only mode", err.Error())
	}
}

func TestGenerateConfigFailsFastWhenMonomindMissing(t *testing.T) {
	t.Setenv(monomind.EnvOverride, filepath.Join(t.TempDir(), "does-not-exist"))

	g := NewAgentGenerator(zerolog.Nop())
	_, err := g.GenerateConfig(context.Background(), "test-config", "<html></html>", "extract title", nil)
	if err == nil {
		t.Fatal("GenerateConfig() = nil error, want a cache-only error when monomind is missing")
	}
	if !strings.Contains(err.Error(), "cache-only mode") {
		t.Errorf("GenerateConfig() error = %q, want it to mention cache-only mode", err.Error())
	}
}
