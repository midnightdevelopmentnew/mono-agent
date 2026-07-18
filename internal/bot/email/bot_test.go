package email

import "testing"

func TestEmailExtractUsername(t *testing.T) {
	b := &EmailBot{}
	cases := map[string]string{
		"mailto:alice@example.com":                    "alice@example.com",
		"mailto:alice@example.com?subject=Hi":         "alice@example.com",
		"bob@example.com":                             "bob@example.com",
		"<carol@example.com>":                         "carol@example.com",
		"":                                            "",
	}
	for in, want := range cases {
		if got := b.ExtractUsername(in); got != want {
			t.Errorf("ExtractUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmailResolveURLPassThrough(t *testing.T) {
	b := &EmailBot{}
	if got := b.ResolveURL("mailto:x@y.com"); got != "mailto:x@y.com" {
		t.Errorf("ResolveURL should pass through, got %q", got)
	}
}

func TestEmailPlatform(t *testing.T) {
	if (&EmailBot{}).Platform() != "EMAIL" {
		t.Error("Platform() should be EMAIL")
	}
}
