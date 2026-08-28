package main

import "testing"

// The default profile must keep 9222 so existing installs, and any Chrome
// extension already configured against that URL, are unaffected.
func TestExtensionPortDefaultUnchanged(t *testing.T) {
	for _, id := range []string{"", "default"} {
		if got := extensionPortForProfile(id); got != 9222 {
			t.Errorf("profile %q: got port %d, want 9222", id, got)
		}
	}
}

// A non-default profile must not collide with the default profile's port,
// or the second browser to connect would evict the first.
func TestExtensionPortNonDefaultAvoids9222(t *testing.T) {
	for _, id := range []string{"linkedin-management", "060c4f32-390f-4222-a9bf-739ea850ee75", "x", "a", "zzzz"} {
		got := extensionPortForProfile(id)
		if got == 9222 {
			t.Errorf("profile %q collided with the default port 9222", id)
		}
		if got < 9223 || got > 9300 {
			t.Errorf("profile %q: port %d outside the 9223-9300 range", id, got)
		}
	}
}

// The mapping must be deterministic across runs: the extension is configured
// by hand once with a ws:// URL and must not need updating.
func TestExtensionPortIsStable(t *testing.T) {
	const id = "linkedin-management"
	first := extensionPortForProfile(id)
	for i := 0; i < 100; i++ {
		if got := extensionPortForProfile(id); got != first {
			t.Fatalf("port not stable: got %d then %d", first, got)
		}
	}
}

func TestConfigureExtensionPortSetsBothAddrs(t *testing.T) {
	defer configureExtensionPort("default")
	configureExtensionPort("linkedin-management")
	wantPort := extensionPortForProfile("linkedin-management")
	if extensionServerAddr != "http://127.0.0.1:"+itoa(wantPort) {
		t.Errorf("addr = %q, want port %d", extensionServerAddr, wantPort)
	}
	if extensionServerBind != "127.0.0.1:"+itoa(wantPort) {
		t.Errorf("bind = %q, want port %d", extensionServerBind, wantPort)
	}
	configureExtensionPort("default")
	if extensionServerBind != "127.0.0.1:9222" {
		t.Errorf("reset to default: bind = %q, want 127.0.0.1:9222", extensionServerBind)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
