package secrets

import (
	"testing"

	"github.com/zalando/go-keyring"
)

func TestGetOrCreateKEK_PersistsAcrossCalls(t *testing.T) {
	keyring.MockInit() // in-memory mock backend, no real OS keychain touched in tests

	key1, err := getOrCreateKEK()
	if err != nil {
		t.Fatalf("getOrCreateKEK (first call): %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("expected 32-byte key, got %d bytes", len(key1))
	}

	key2, err := getOrCreateKEK()
	if err != nil {
		t.Fatalf("getOrCreateKEK (second call): %v", err)
	}
	if string(key1) != string(key2) {
		t.Fatal("second call must return the same KEK, not regenerate")
	}
}
