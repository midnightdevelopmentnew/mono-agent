package x

import "testing"

func TestXResolveURL(t *testing.T) {
	b := &XBot{}
	if got := b.ResolveURL("/username"); got != "https://x.com/username" {
		t.Errorf("ResolveURL relative = %q", got)
	}
	if got := b.ResolveURL("https://x.com/username"); got != "https://x.com/username" {
		t.Errorf("ResolveURL absolute changed: %q", got)
	}
}

func TestXExtractUsername(t *testing.T) {
	b := &XBot{}
	cases := map[string]string{
		"https://x.com/jack":            "jack",
		"https://x.com/jack/status/123": "jack",
		"https://x.com/home":            "", // reserved
		"https://x.com/notifications":   "", // reserved
		"https://x.com/messages":        "", // reserved
		"https://x.com/":                "",
		"":                              "",
	}
	for in, want := range cases {
		if got := b.ExtractUsername(in); got != want {
			t.Errorf("ExtractUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestXPlatform(t *testing.T) {
	if (&XBot{}).Platform() != "X" {
		t.Error("Platform() should be X")
	}
}
