package monomind

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOrgListAgainstFake(t *testing.T) {
	os.Setenv(EnvOverride, fakeBin(t, "fake-monomind.sh"))
	defer os.Unsetenv(EnvOverride)

	out, err := OrgList(context.Background(), ".")
	if err != nil {
		t.Fatalf("OrgList: %v", err)
	}
	if !strings.Contains(string(out), `"growth"`) {
		t.Errorf("OrgList output = %s, want it to contain the fake org", out)
	}
}

func TestOrgStatusAgainstFake(t *testing.T) {
	os.Setenv(EnvOverride, fakeBin(t, "fake-monomind.sh"))
	defer os.Unsetenv(EnvOverride)

	out, err := OrgStatus(context.Background(), ".", "growth")
	if err != nil {
		t.Fatalf("OrgStatus: %v", err)
	}
	if !strings.Contains(string(out), `"running"`) {
		t.Errorf("OrgStatus output = %s, want status running", out)
	}
}

func TestOrgStatusErrorSurfacesStderr(t *testing.T) {
	os.Setenv(EnvOverride, fakeBin(t, "fake-monomind.sh"))
	defer os.Unsetenv(EnvOverride)

	_, err := OrgStatus(context.Background(), ".", "missing-org")
	if err == nil {
		t.Fatal("OrgStatus() = nil error, want an error for a nonexistent org")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("OrgStatus() error = %q, want it to surface the fake's stderr message", err.Error())
	}
}

func TestOrgEventsStreamsLines(t *testing.T) {
	os.Setenv(EnvOverride, fakeBin(t, "fake-monomind.sh"))
	defer os.Unsetenv(EnvOverride)

	var lines [][]byte
	err := OrgEvents(context.Background(), ".", "growth", OrgEventsOptions{}, func(line []byte) {
		cp := make([]byte, len(line))
		copy(cp, line)
		lines = append(lines, cp)
	})
	if err != nil {
		t.Fatalf("OrgEvents: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("OrgEvents delivered %d lines, want 2", len(lines))
	}
	if !strings.Contains(string(lines[0]), `"e1"`) {
		t.Errorf("first line = %s, want it to contain event e1", lines[0])
	}
}

// TestOrgEventsAbortsPromptlyOnCancelDuringHandshake guards against OrgEvents
// hanging forever when the caller cancels before the subprocess even starts
// streaming: fake-monolith.sh never answers `--version --json`, so without
// ctx propagation through Ensure()'s handshake this would block until the
// script's internal 60s sleep, not return promptly.
func TestOrgEventsAbortsPromptlyOnCancelDuringHandshake(t *testing.T) {
	os.Setenv(EnvOverride, fakeBin(t, "fake-monolith.sh"))
	defer os.Unsetenv(EnvOverride)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- OrgEvents(ctx, ".", "growth", OrgEventsOptions{Follow: true}, func([]byte) {})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("OrgEvents() = nil error with an already-cancelled ctx, want an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OrgEvents() did not return within 5s of ctx cancellation during handshake")
	}
}
